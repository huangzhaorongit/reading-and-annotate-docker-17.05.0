package distribution

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/url"
	"os"
	"runtime"

	"github.com/Sirupsen/logrus"
	"github.com/docker/distribution"
	"github.com/docker/distribution/manifest/manifestlist"
	"github.com/docker/distribution/manifest/schema1"
	"github.com/docker/distribution/manifest/schema2"
	"github.com/docker/distribution/reference"
	"github.com/docker/distribution/registry/api/errcode"
	"github.com/docker/distribution/registry/client/auth"
	"github.com/docker/distribution/registry/client/transport"
	"github.com/docker/docker/distribution/metadata"
	"github.com/docker/docker/distribution/xfer"
	"github.com/docker/docker/image"
	"github.com/docker/docker/image/v1"
	"github.com/docker/docker/layer"
	"github.com/docker/docker/pkg/ioutils"
	"github.com/docker/docker/pkg/progress"
	"github.com/docker/docker/pkg/stringid"
	refstore "github.com/docker/docker/reference"
	"github.com/docker/docker/registry"
	"github.com/opencontainers/go-digest"
	"golang.org/x/net/context"
)

var (
	errRootFSMismatch = errors.New("layers from manifest don't match image configuration")
	errRootFSInvalid  = errors.New("invalid rootfs in image configuration")
)

// ImageConfigPullError is an error pulling the image config blob
// (only applies to schema2).
type ImageConfigPullError struct {
	Err error
}

// Error returns the error string for ImageConfigPullError.
func (e ImageConfigPullError) Error() string {
	return "error pulling image configuration: " + e.Err.Error()
}

//newPuller 中构造使用
type v2Puller struct {
	//对应 v2MetadataService 结构
	V2MetadataService metadata.V2MetadataService
	endpoint          registry.APIEndpoint
	config            *ImagePullConfig
	repoInfo          *registry.RepositoryInfo
	//赋值见(p *v2Puller) Pull  //registry\client\repository.go   repository 结构
	repo              distribution.Repository
	// confirmedV2 is set to true if we confirm we're talking to a v2
	// registry. This is used to limit fallbacks to the v1 protocol.
	confirmedV2 bool //是否支持V2  赋值见(p *v2Puller) Pull
}

//V2版本的Puller
func (p *v2Puller) Pull(ctx context.Context, ref reference.Named) (err error) {
	// TODO(tiborvass): was ReceiveTimeout
	//registry\client\repository.go  返回 repository 结构  获取V2仓库信息
	p.repo, p.confirmedV2, err = NewV2Repository(ctx, p.repoInfo, p.endpoint, p.config.MetaHeaders, p.config.AuthConfig, "pull")
	if err != nil {
		logrus.Warnf("Error getting v2 registry: %v", err)
		return err
	}

	if err = p.pullV2Repository(ctx, ref); err != nil {
		if _, ok := err.(fallbackError); ok {
			return err
		}
		if continueOnError(err) {
			return fallbackError{
				err:         err,
				confirmedV2: p.confirmedV2,
				transportOK: true,
			}
		}
	}
	return err
}

//一个repository可能对应着多个tag，docker的镜像呈现的是树形关系，比如ubuntu是一个repository，实际的存储情况是可能会有一个基础镜像base，
// 这个基础镜像上，增加一些新的内容（实际上就是增加一个新的读写层，写入东西进去）就会形成新的镜像，比如：ubuntu:12.12是一个镜像，那么
// ubuntu:14.01是在前者基础上进行若干修改操作而形成的新的镜像；所以要下载ubuntu:14.01这个镜像的话，必须要将其父镜像完全下载下来，这样下载之后的镜像才是完整的；

//新建一个V2版本的仓库赋值给p.repo，调用pullV2Respository函数,
func (p *v2Puller) pullV2Repository(ctx context.Context, ref reference.Named) (err error) {
	var layersDownloaded bool
	if !reference.IsNameOnly(ref) {
		//pullV2Tag才是真正的最后的下载环节
		layersDownloaded, err = p.pullV2Tag(ctx, ref)
		if err != nil {
			return err
		}
	} else {
		tags, err := p.repo.Tags(ctx).All(ctx)
		if err != nil {
			// If this repository doesn't exist on V2, we should
			// permit a fallback to V1.
			return allowV1Fallback(err)
		}

		// The v2 registry knows about this repository, so we will not
		// allow fallback to the v1 protocol even if we encounter an
		// error later on.
		p.confirmedV2 = true

		for _, tag := range tags {
			tagRef, err := reference.WithTag(ref, tag)
			if err != nil {
				return err
			}

			//pullV2Tag才是真正的最后的下载环节
			pulledNew, err := p.pullV2Tag(ctx, tagRef)
			if err != nil {
				// Since this is the pull-all-tags case, don't
				// allow an error pulling a particular tag to
				// make the whole pull fall back to v1.
				if fallbackErr, ok := err.(fallbackError); ok {
					return fallbackErr.err
				}
				return err
			}
			// pulledNew is true if either new layers were downloaded OR if existing images were newly tagged
			// TODO(tiborvass): should we change the name of `layersDownload`? What about message in WriteStatus?
			layersDownloaded = layersDownloaded || pulledNew
		}
	}

	writeStatus(reference.FamiliarString(ref), p.config.ProgressOutput, layersDownloaded)

	return nil
}

/*
{
   "schemaVersion": 2,
   "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
   "config": {
      "mediaType": "application/vnd.docker.container.image.v1+json",
      "size": 5836,
      "digest": "sha256:40960efd7b8f44ed5cafee61c189a8f4db39838848d41861898f56c29565266e"
   },
   "layers": [
      {
         "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
         "size": 22492350,
         "digest": "sha256:bc95e04b23c06ba1b9bf092d07d1493177b218e0340bd2ed49dac351c1e34313"
      },
      {
         "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
         "size": 21913353,
         "digest": "sha256:a21d9ee25fc3dcef76028536e7191e44554a8088250d4c3ec884af23cef4f02a"
      },
      {
         "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
         "size": 202,
         "digest": "sha256:9bda7d5afd399f51550422c49172f8c9169fc3ffdef2748b13cfbf6467661ac5"
      }
   ]
}
*/
//(p *v2Puller) pullSchema2 中构造使用
type v2LayerDescriptor struct {
	//manifest内容中的 layers 层中的digest   上面注释中 layers 中的digest
	digest            digest.Digest
	//v2Puller.repoInfo
	repoInfo          *registry.RepositoryInfo
	//v2Puller.repo
	repo              distribution.Repository
	//v2Puller.V2MetadataService   //对应 v2MetadataService 结构
	V2MetadataService metadata.V2MetadataService
	tmpFile           *os.File
	verifier          digest.Verifier
	//代表上面注释中的manifest中的一小个内容
	src               distribution.Descriptor
}

func (ld *v2LayerDescriptor) Key() string {
	return "v2:" + ld.digest.String()
}

func (ld *v2LayerDescriptor) ID() string {
	return stringid.TruncateID(ld.digest.String())
}

//根据digest算出diffid
func (ld *v2LayerDescriptor) DiffID() (layer.DiffID, error) {
	//(serv *v2MetadataService) GetDiffID
	return ld.V2MetadataService.GetDiffID(ld.digest)
}

//(ldm *LayerDownloadManager) makeDownloadFunc 中执行该函数
func (ld *v2LayerDescriptor) Download(ctx context.Context, progressOutput progress.Output) (io.ReadCloser, int64, error) {
	logrus.Debugf("pulling blob %q", ld.digest)

	var (
		err    error
		offset int64
	)

	if ld.tmpFile == nil {
		ld.tmpFile, err = createDownloadFile()
		if err != nil {
			return nil, 0, xfer.DoNotRetry{Err: err}
		}
	} else {
		offset, err = ld.tmpFile.Seek(0, os.SEEK_END)
		if err != nil {
			logrus.Debugf("error seeking to end of download file: %v", err)
			offset = 0

			ld.tmpFile.Close()
			if err := os.Remove(ld.tmpFile.Name()); err != nil {
				logrus.Errorf("Failed to remove temp file: %s", ld.tmpFile.Name())
			}
			ld.tmpFile, err = createDownloadFile()
			if err != nil {
				return nil, 0, xfer.DoNotRetry{Err: err}
			}
		} else if offset != 0 {
			logrus.Debugf("attempting to resume download of %q from %d bytes", ld.digest, offset)
		}
	}

	tmpFile := ld.tmpFile

	layerDownload, err := ld.open(ctx)
	if err != nil {
		logrus.Errorf("Error initiating layer download: %v", err)
		return nil, 0, retryOnError(err)
	}

	if offset != 0 {
		_, err := layerDownload.Seek(offset, os.SEEK_SET)
		if err != nil {
			if err := ld.truncateDownloadFile(); err != nil {
				return nil, 0, xfer.DoNotRetry{Err: err}
			}
			return nil, 0, err
		}
	}
	size, err := layerDownload.Seek(0, os.SEEK_END)
	if err != nil {
		// Seek failed, perhaps because there was no Content-Length
		// header. This shouldn't fail the download, because we can
		// still continue without a progress bar.
		size = 0
	} else {
		if size != 0 && offset > size {
			logrus.Debug("Partial download is larger than full blob. Starting over")
			offset = 0
			if err := ld.truncateDownloadFile(); err != nil {
				return nil, 0, xfer.DoNotRetry{Err: err}
			}
		}

		// Restore the seek offset either at the beginning of the
		// stream, or just after the last byte we have from previous
		// attempts.
		_, err = layerDownload.Seek(offset, os.SEEK_SET)
		if err != nil {
			return nil, 0, err
		}
	}

	reader := progress.NewProgressReader(ioutils.NewCancelReadCloser(ctx, layerDownload), progressOutput, size-offset, ld.ID(), "Downloading")
	defer reader.Close()

	if ld.verifier == nil {
		ld.verifier = ld.digest.Verifier()
	}

	_, err = io.Copy(tmpFile, io.TeeReader(reader, ld.verifier))
	if err != nil {
		if err == transport.ErrWrongCodeForByteRange {
			if err := ld.truncateDownloadFile(); err != nil {
				return nil, 0, xfer.DoNotRetry{Err: err}
			}
			return nil, 0, err
		}
		return nil, 0, retryOnError(err)
	}

	progress.Update(progressOutput, ld.ID(), "Verifying Checksum")

	if !ld.verifier.Verified() {
		err = fmt.Errorf("filesystem layer verification failed for digest %s", ld.digest)
		logrus.Error(err)

		// Allow a retry if this digest verification error happened
		// after a resumed download.
		if offset != 0 {
			if err := ld.truncateDownloadFile(); err != nil {
				return nil, 0, xfer.DoNotRetry{Err: err}
			}

			return nil, 0, err
		}
		return nil, 0, xfer.DoNotRetry{Err: err}
	}

	progress.Update(progressOutput, ld.ID(), "Download complete")

	logrus.Debugf("Downloaded %s to tempfile %s", ld.ID(), tmpFile.Name())

	_, err = tmpFile.Seek(0, os.SEEK_SET)
	if err != nil {
		tmpFile.Close()
		if err := os.Remove(tmpFile.Name()); err != nil {
			logrus.Errorf("Failed to remove temp file: %s", tmpFile.Name())
		}
		ld.tmpFile = nil
		ld.verifier = nil
		return nil, 0, xfer.DoNotRetry{Err: err}
	}

	// hand off the temporary file to the download manager, so it will only
	// be closed once
	ld.tmpFile = nil

	return ioutils.NewReadCloserWrapper(tmpFile, func() error {
		tmpFile.Close()
		err := os.RemoveAll(tmpFile.Name())
		if err != nil {
			logrus.Errorf("Failed to remove temp file: %s", tmpFile.Name())
		}
		return err
	}), size, nil
}

func (ld *v2LayerDescriptor) Close() {
	if ld.tmpFile != nil {
		ld.tmpFile.Close()
		if err := os.RemoveAll(ld.tmpFile.Name()); err != nil {
			logrus.Errorf("Failed to remove temp file: %s", ld.tmpFile.Name())
		}
	}
}

func (ld *v2LayerDescriptor) truncateDownloadFile() error {
	// Need a new hash context since we will be redoing the download
	ld.verifier = nil

	if _, err := ld.tmpFile.Seek(0, os.SEEK_SET); err != nil {
		logrus.Errorf("error seeking to beginning of download file: %v", err)
		return err
	}

	if err := ld.tmpFile.Truncate(0); err != nil {
		logrus.Errorf("error truncating download file: %v", err)
		return err
	}

	return nil
}

func (ld *v2LayerDescriptor) Registered(diffID layer.DiffID) {
	// Cache mapping from this layer's DiffID to the blobsum
	// (serv *v2MetadataService) Add
	ld.V2MetadataService.Add(diffID, metadata.V2Metadata{Digest: ld.digest, SourceRepository: ld.repoInfo.Name.Name()})
}

/*
DEBU[2829] Trying to pull nginx from http://a9e61d46.m.daocloud.io/ v2
DEBU[2830] Pulling ref from V2 registry: nginx:latest
DEBU[2830] docker.io/library/nginx:latest resolved to a manifestList object with 6 entries; looking for a os/arch match
DEBU[2830] found match for linux/amd64 with media type application/vnd.docker.distribution.manifest.v2+json, digest sha256:278fefc722ffe1c36f6dd64052758258d441dcdb5e1bbbed0670485af2413c9f
DEBU[2830] pulling blob "sha256:a21d9ee25fc3dcef76028536e7191e44554a8088250d4c3ec884af23cef4f02a"
DEBU[2830] pulling blob "sha256:bc95e04b23c06ba1b9bf092d07d1493177b218e0340bd2ed49dac351c1e34313"
DEBU[2830] pulling blob "sha256:9bda7d5afd399f51550422c49172f8c9169fc3ffdef2748b13cfbf6467661ac5"
DEBU[2830] Downloaded 9bda7d5afd39 to tempfile /var/lib/docker/tmp/GetImageBlob443166429
DEBU[2833] Downloaded a21d9ee25fc3 to tempfile /var/lib/docker/tmp/GetImageBlob798345779
DEBU[2833] Downloaded bc95e04b23c0 to tempfile /var/lib/docker/tmp/GetImageBlob345344502
DEBU[2833] Start untar layer
DEBU[2834] Untar time: 0.8172344880000001s
DEBU[2834] Applied tar sha256:cec7521cdf36a6d4ad8f2e92e41e3ac1b6fa6e05be07fa53cc84a63503bc5700 to 10275c2bd04bccf79c656e845a7c8338b55c0199206e4cd9acb0b4a143d6e7bb, size: 55248856
DEBU[2835] Applied tar sha256:bba7659ae2e77090e21f86e69d199aff4fcf9c71204fa3f3d5c89463c43eae7e to d84edcc88d62d2cc3cbc2fbd6a51017f65d931ea3e7642737a0f2ec6790425cb, size: 53113283
DEBU[2835] Applied tar sha256:f4cc3366d6a98b6cf5d047ad113ba76f67e4f5d20d12e37cd9a1f34ce0b421b9 to e5ffb3609c528569dc948de7026ec24d72c6bb4782e4770af730a59b0646666d, size: 0
*/

//有tag的话，遍历tag，每个tag调用pullV2Tag，没有的话直接调用pullV2Tag
func (p *v2Puller) pullV2Tag(ctx context.Context, ref reference.Named) (tagUpdated bool, err error) {
	//获取 manifest 属性，根据属性的类型做不同的处理
	// (r *repository) Manifests 中构造 manifests 结构
	manSvc, err := p.repo.Manifests(ctx) //获取首选项的mainfest服务
	if err != nil {
		return false, err
	}

	var (
		manifest    distribution.Manifest
		tagOrDigest string // Used for logging/progress only
	)

	if tagged, isTagged := ref.(reference.NamedTagged); isTagged { //是否带tag
		//例如 docker pull mysql:21201010
		//(r *manifests) Get  //获取manifests 文件内容，然后反序列化存入  Manifest 结构
		manifest, err = manSvc.Get(ctx, "", distribution.WithTag(tagged.Tag()))
		if err != nil {
			return false, allowV1Fallback(err)
		}
		tagOrDigest = tagged.Tag()
	} else if digested, isDigested := ref.(reference.Canonical); isDigested { //是否请求通过的是digeste方式
		//例如docker pull mysql@sha256:89cc6ff6a7ac9916c3384e864fb04b8ee9415b572f872a2a4cf5b909dbbca81b
		//(r *manifests) Get  //获取manifests 文件内容，然后反序列化存入  Manifest 结构
		manifest, err = manSvc.Get(ctx, digested.Digest())
		if err != nil {
			return false, err
		}
		tagOrDigest = digested.Digest().String()
	} else {
		return false, fmt.Errorf("internal error: reference has neither a tag nor a digest: %s", reference.FamiliarString(ref))
	}

	if manifest == nil {
		return false, fmt.Errorf("image manifest does not exist for tag or digest %q", tagOrDigest)
	}

	//manifest 对应的是schema2  V2
	if m, ok := manifest.(*schema2.DeserializedManifest); ok {
		var allowedMediatype bool
		for _, t := range p.config.Schema2Types {
			if m.Manifest.Config.MediaType == t {
				allowedMediatype = true
				break
			}
		}
		if !allowedMediatype {
			configClass := mediaTypeClasses[m.Manifest.Config.MediaType]
			if configClass == "" {
				configClass = "unknown"
			}
			return false, fmt.Errorf("Encountered remote %q(%s) when fetching", m.Manifest.Config.MediaType, configClass)
		}
	}

	// If manSvc.Get succeeded, we can be confident that the registry on
	// the other side speaks the v2 protocol.
	p.confirmedV2 = true

	logrus.Debugf("Pulling ref from V2 registry: %s， repo.named:%s", reference.FamiliarString(ref), p.repo.Named())
	progress.Message(p.config.ProgressOutput, tagOrDigest, "Pulling from "+reference.FamiliarName(p.repo.Named()))

	var (
		id             digest.Digest
		manifestDigest digest.Digest
	)

	switch v := manifest.(type) {
	case *schema1.SignedManifest:
		if p.config.RequireSchema2 {
			return false, fmt.Errorf("invalid manifest: not schema2")
		}
		id, manifestDigest, err = p.pullSchema1(ctx, ref, v)
		if err != nil {
			return false, err
		}
	case *schema2.DeserializedManifest:
		id, manifestDigest, err = p.pullSchema2(ctx, ref, v)
		if err != nil {
			return false, err
		}
	case *manifestlist.DeserializedManifestList:
		id, manifestDigest, err = p.pullManifestList(ctx, ref, v)
		if err != nil {
			return false, err
		}
	default:
		return false, errors.New("unsupported manifest format")
	}

	progress.Message(p.config.ProgressOutput, "", "Digest: "+manifestDigest.String())

	if p.config.ReferenceStore != nil {
		oldTagID, err := p.config.ReferenceStore.Get(ref)
		if err == nil {
			if oldTagID == id {
				return false, addDigestReference(p.config.ReferenceStore, ref, manifestDigest, id)
			}
		} else if err != refstore.ErrDoesNotExist {
			return false, err
		}

		if canonical, ok := ref.(reference.Canonical); ok {
			if err = p.config.ReferenceStore.AddDigest(canonical, id, true); err != nil {
				return false, err
			}
		} else {
			if err = addDigestReference(p.config.ReferenceStore, ref, manifestDigest, id); err != nil {
				return false, err
			}
			if err = p.config.ReferenceStore.AddTag(ref, id, true); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

//下载镜像  (p *v2Puller) pullV2Tag 中执行
func (p *v2Puller) pullSchema1(ctx context.Context, ref reference.Named, unverifiedManifest *schema1.SignedManifest) (id digest.Digest, manifestDigest digest.Digest, err error) {
	var verifiedManifest *schema1.Manifest
	verifiedManifest, err = verifySchema1Manifest(unverifiedManifest, ref)
	if err != nil {
		return "", "", err
	}

	rootFS := image.NewRootFS()

	// remove duplicate layers and check parent chain validity
	err = fixManifestLayers(verifiedManifest)
	if err != nil {
		return "", "", err
	}

	var descriptors []xfer.DownloadDescriptor

	// Image history converted to the new format
	var history []image.History

	// Note that the order of this loop is in the direction of bottom-most
	// to top-most, so that the downloads slice gets ordered correctly.
	for i := len(verifiedManifest.FSLayers) - 1; i >= 0; i-- {
		blobSum := verifiedManifest.FSLayers[i].BlobSum

		var throwAway struct {
			ThrowAway bool `json:"throwaway,omitempty"`
		}
		if err := json.Unmarshal([]byte(verifiedManifest.History[i].V1Compatibility), &throwAway); err != nil {
			return "", "", err
		}

		h, err := v1.HistoryFromConfig([]byte(verifiedManifest.History[i].V1Compatibility), throwAway.ThrowAway)
		if err != nil {
			return "", "", err
		}
		history = append(history, h)

		if throwAway.ThrowAway {
			continue
		}

		layerDescriptor := &v2LayerDescriptor{
			digest:            blobSum,
			repoInfo:          p.repoInfo,
			repo:              p.repo,
			V2MetadataService: p.V2MetadataService,
		}

		descriptors = append(descriptors, layerDescriptor)
	}

	//下载镜像 (ldm *LayerDownloadManager) Download
	resultRootFS, release, err := p.config.DownloadManager.Download(ctx, *rootFS, descriptors, p.config.ProgressOutput)
	if err != nil {
		return "", "", err
	}
	defer release()

	config, err := v1.MakeConfigFromV1Config([]byte(verifiedManifest.History[0].V1Compatibility), &resultRootFS, history)
	if err != nil {
		return "", "", err
	}

	imageID, err := p.config.ImageStore.Put(config)
	if err != nil {
		return "", "", err
	}

	manifestDigest = digest.FromBytes(unverifiedManifest.Canonical)

	return imageID, manifestDigest, nil
}

/*
docker pull的大概过程
如果对Image manifest，Image Config和Filesystem Layers等概念不是很了解，请先参考image(镜像)是什么。

取image的大概过程如下：

docker发送image的名称+tag（或者digest）给registry服务器，服务器根据收到的image的名称+tag（或者digest），找到相应image的manifest，然后将manifest返回给docker
docker得到manifest后，读取里面image配置文件的digest(sha256)，这个sha256码就是image的ID
根据ID在本地找有没有存在同样ID的image，有的话就不用继续下载了
如果没有，那么会给registry服务器发请求（里面包含配置文件的sha256和media type），拿到image的配置文件（Image Config）
根据配置文件中的diff_ids（每个diffid对应一个layer tar包的sha256，tar包相当于layer的原始格式），在本地找对应的layer是否存在
如果layer不存在，则根据manifest里面layer的sha256和media type去服务器拿相应的layer（相当去拿压缩格式的包）。
拿到后进行解压，并检查解压后tar包的sha256能否和配置文件（Image Config）中的diff_id对的上，对不上说明有问题，下载失败
根据docker所用的后台文件系统类型，解压tar包并放到指定的目录
等所有的layer都下载完成后，整个image下载完成，就可以使用了
注意： 对于layer来说，config文件中diffid是layer的tar包的sha256，而manifest文件中的digest依赖于media type，比如media type是tar+gzip，
那digest就是layer的tar包经过gzip压缩后的内容的sha256，如果media type就是tar的话，diffid和digest就会一样。
*/
//下载镜像 (p *v2Puller) pullV2Tag 中执行
func (p *v2Puller) pullSchema2(ctx context.Context, ref reference.Named, mfst *schema2.DeserializedManifest) (id digest.Digest, manifestDigest digest.Digest, err error) {
	//返回HTTP请求的 manifest 包体内容
	manifestDigest, err = schema2ManifestDigest(ref, mfst)
	if err != nil {
		return "", "", err
	}

	/*  manifest文件内容 (ms *manifests) Get 函数获取manifest文件，并打印内容
	{
	   ......
	   "config": {
	      "mediaType": "application/vnd.docker.container.image.v1+json",
	      "size": 5836,
	      "digest": "sha256:40960efd7b8f44ed5cafee61c189a8f4db39838848d41861898f56c29565266e"
	   },
	   "layers": [
	      ....
	   ]
	}
	*/
	//schema2\manifest.go 中的 (m Manifest) Target()
	//获取manifest中的Config信息
	target := mfst.Target() //获取 DeserializedManifest.Manifest.Config 内容

	//查询镜像配置，如果 digest 已经存在直接返回  (is *store) Get
	// 检查/var/lib/docker/image/devicemapper/imagedb/content/sha256目录是否有该digest存在
	//docker得到manifest后，读取里面image配置文件的digest(sha256)，这个sha256码就是image的ID,判断该imageID是否已经存在
	if _, err := p.config.ImageStore.Get(target.Digest); err == nil {
		// If the image already exists locally, no need to pull
		// anything.
		return target.Digest, manifestDigest, nil
	}

	var descriptors []xfer.DownloadDescriptor

	// Note that the order of this loop is in the direction of bottom-most
	// to top-most, so that the downloads slice gets ordered correctly.
	/*  manifest文件内容 (ms *manifests) Get 函数获取manifest文件，并打印内容
	{
	   ......
	   "layers": [
	      {
		 "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
		 "size": 22492350,
		 "digest": "sha256:bc95e04b23c06ba1b9bf092d07d1493177b218e0340bd2ed49dac351c1e34313"
	      },
	      {
		 "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
		 "size": 21913353,
		 "digest": "sha256:a21d9ee25fc3dcef76028536e7191e44554a8088250d4c3ec884af23cef4f02a"
	      },
	      {
		 "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
		 "size": 202,
		 "digest": "sha256:9bda7d5afd399f51550422c49172f8c9169fc3ffdef2748b13cfbf6467661ac5"
	      }
	   ]
	}
	*/
	for _, d := range mfst.Layers {
		layerDescriptor := &v2LayerDescriptor{
			//manifest内容中的 layers 层中的digest
			digest:            d.Digest,
			repo:              p.repo,
			repoInfo:          p.repoInfo,
			V2MetadataService: p.V2MetadataService,
			src:               d,
		}

		//manifest内容中的"layers"中对应所有的layers信息存入 descriptors
		descriptors = append(descriptors, layerDescriptor)
	}

	configChan := make(chan []byte, 1)
	configErrChan := make(chan error, 1)
	layerErrChan := make(chan error, 1)
	downloadsDone := make(chan struct{})
	var cancel func()
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()

	// Pull the image config
	go func() {
		//根据 manifest 文件中的"config" {} 中的digest来获取
		//给registry服务器发请求（里面包含配置文件的sha256和media type），拿到image的配置文件（Image Config）
		//返回的也就是 /var/lib/docker/image/devicemapper/imagedb/content/sha256/$imageid中的内容
		configJSON, err := p.pullSchema2Config(ctx, target.Digest)
		if err != nil {
			configErrChan <- ImageConfigPullError{Err: err}
			cancel()
			return
		}

		//fmt.Println(string(configJSON)) //yang test
		//在后面的 p.config.ImageStore.Put(configJSON) 创建/var/lib/docker/image/devicemapper/imagedb/content/sha256/$image并把相关image config内容写进去
		configChan <- configJSON
	}()

	var (
		configJSON       []byte        // raw serialized image config
		downloadedRootFS *image.RootFS // rootFS from registered layers
		configRootFS     *image.RootFS // rootFS from configuration
		release          func()        // release resources from rootFS download
	)

	// https://github.com/docker/docker/issues/24766 - Err on the side of caution,
	// explicitly blocking images intended for linux from the Windows daemon. On
	// Windows, we do this before the attempt to download, effectively serialising
	// the download slightly slowing it down. We have to do it this way, as
	// chances are the download of layers itself would fail due to file names
	// which aren't suitable for NTFS. At some point in the future, if a similar
	// check to block Windows images being pulled on Linux is implemented, it
	// may be necessary to perform the same type of serialisation.
	if runtime.GOOS == "windows" {
		configJSON, configRootFS, err = receiveConfig(p.config.ImageStore, configChan, configErrChan)
		if err != nil {
			return "", "", err
		}

		if configRootFS == nil {
			return "", "", errRootFSInvalid
		}
	}

	if p.config.DownloadManager != nil {
		go func() {
			var (
				err    error
				rootFS image.RootFS
			)
			downloadRootFS := *image.NewRootFS()
			//下载镜像 (ldm *LayerDownloadManager) Download
			//downloadRootFS 对应一个新的RootFS结构，descriptors 对应manifest内容中的"layers"相关的layer信息(v2LayerDescriptor结构存储)， ImagePullConfig.Config.ProgressOutput
			rootFS, release, err = p.config.DownloadManager.Download(ctx, downloadRootFS, descriptors, p.config.ProgressOutput)
			if err != nil {
				// Intentionally do not cancel the config download here
				// as the error from config download (if there is one)
				// is more interesting than the layer download error
				layerErrChan <- err
				return
			}

			downloadedRootFS = &rootFS
			close(downloadsDone)
		}()
	} else {
		// We have nothing to download
		close(downloadsDone)
	}

	if configJSON == nil {
		configJSON, configRootFS, err = receiveConfig(p.config.ImageStore, configChan, configErrChan)
		if err == nil && configRootFS == nil {
			err = errRootFSInvalid
		}
		if err != nil {
			cancel()
			select {
			case <-downloadsDone:
			case <-layerErrChan:
			}
			return "", "", err
		}
	}

	select {
	case <-downloadsDone:
	case err = <-layerErrChan:
		return "", "", err
	}

	if release != nil {
		defer release()
	}

	if downloadedRootFS != nil {
		// The DiffIDs returned in rootFS MUST match those in the config.
		// Otherwise the image config could be referencing layers that aren't
		// included in the manifest.
		if len(downloadedRootFS.DiffIDs) != len(configRootFS.DiffIDs) {
			return "", "", errRootFSMismatch
		}

		for i := range downloadedRootFS.DiffIDs {
			if downloadedRootFS.DiffIDs[i] != configRootFS.DiffIDs[i] {
				return "", "", errRootFSMismatch
			}
		}
	}

	/////var/lib/docker/image/devicemapper/imagedb/content/sha256/$image创建并把相关image config内容写进去
	//(s *imageConfigStore) Put
	imageID, err := p.config.ImageStore.Put(configJSON)
	if err != nil {
		return "", "", err
	}

	return imageID, manifestDigest, nil
}

func receiveConfig(s ImageConfigStore, configChan <-chan []byte, errChan <-chan error) ([]byte, *image.RootFS, error) {
	select {
	case configJSON := <-configChan:
		rootfs, err := s.RootFSFromConfig(configJSON)
		if err != nil {
			return nil, nil, err
		}
		return configJSON, rootfs, nil
	case err := <-errChan:
		return nil, nil, err
		// Don't need a case for ctx.Done in the select because cancellation
		// will trigger an error in p.pullSchema2ImageConfig.
	}
}

// pullManifestList handles "manifest lists" which point to various
// platform-specific manifests.

//pullManifestList 最终调用 pullSchema1或者pullSchema2
func (p *v2Puller) pullManifestList(ctx context.Context, ref reference.Named, mfstList *manifestlist.DeserializedManifestList) (id digest.Digest, manifestListDigest digest.Digest, err error) {
	manifestListDigest, err = schema2ManifestDigest(ref, mfstList)
	if err != nil {
		return "", "", err
	}

	logrus.Debugf("%s resolved to a manifestList object with %d entries; looking for a os/arch match", ref, len(mfstList.Manifests))
	var manifestDigest digest.Digest
	for _, manifestDescriptor := range mfstList.Manifests {
		// TODO(aaronl): The manifest list spec supports optional
		// "features" and "variant" fields. These are not yet used.
		// Once they are, their values should be interpreted here.
		if manifestDescriptor.Platform.Architecture == runtime.GOARCH && manifestDescriptor.Platform.OS == runtime.GOOS {
			manifestDigest = manifestDescriptor.Digest
			logrus.Debugf("found match for %s/%s with media type %s, digest %s", runtime.GOOS, runtime.GOARCH, manifestDescriptor.MediaType, manifestDigest.String())
			break
		}
	}

	if manifestDigest == "" {
		errMsg := fmt.Sprintf("no matching manifest for %s/%s in the manifest list entries", runtime.GOOS, runtime.GOARCH)
		logrus.Debugf(errMsg)
		return "", "", errors.New(errMsg)
	}

	manSvc, err := p.repo.Manifests(ctx)
	if err != nil {
		return "", "", err
	}

	manifest, err := manSvc.Get(ctx, manifestDigest)
	if err != nil {
		return "", "", err
	}

	manifestRef, err := reference.WithDigest(reference.TrimNamed(ref), manifestDigest)
	if err != nil {
		return "", "", err
	}

	//下载镜像  V2  或者 V1 方式下载 //下载镜像 (ldm *LayerDownloadManager) Download
	switch v := manifest.(type) {
	case *schema1.SignedManifest:
		id, _, err = p.pullSchema1(ctx, manifestRef, v)
		if err != nil {
			return "", "", err
		}
	case *schema2.DeserializedManifest:
		id, _, err = p.pullSchema2(ctx, manifestRef, v)
		if err != nil {
			return "", "", err
		}
	default:
		return "", "", errors.New("unsupported manifest format")
	}

	return id, manifestListDigest, err
}

/*
{
    "architecture": "amd64",
    "config": {
        "Hostname": "",
        "Domainname": "",
        "User": "",
        "AttachStdin": false,
        "AttachStdout": false,
        "AttachStderr": false,
        "ExposedPorts": {
            "80/tcp": {}
        },
        "Tty": false,
        "OpenStdin": false,
        "StdinOnce": false,
        "Env": [
            "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
            "NGINX_VERSION=1.13.6-1~stretch",
            "NJS_VERSION=1.13.6.0.1.14-1~stretch"
        ],
        "Cmd": [
            "nginx",
            "-g",
            "daemon off;"
        ],
        "ArgsEscaped": true,
        "Image": "sha256:03243c86770b0f1423bf50f17d6cb7841d378d3029efb57c07e34163bf01dbc8",
        "Volumes": null,
        "WorkingDir": "",
        "Entrypoint": null,
        "OnBuild": [],
        "Labels": {
            "maintainer": "NGINX Docker Maintainers <docker-maint@nginx.com>"
        },
        "StopSignal": "SIGTERM"
    },
    "container": "753ba8de84559c416ec91f5d86a9fa385194ea2d90a7b21853bbf8db2315dc60",
    "container_config": {
        "Hostname": "753ba8de8455",
        "Domainname": "",
        "User": "",
        "AttachStdin": false,
        "AttachStdout": false,
        "AttachStderr": false,
        "ExposedPorts": {
            "80/tcp": {}
        },
        "Tty": false,
        "OpenStdin": false,
        "StdinOnce": false,
        "Env": [
            "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
            "NGINX_VERSION=1.13.6-1~stretch",
            "NJS_VERSION=1.13.6.0.1.14-1~stretch"
        ],
        "Cmd": [
            "/bin/sh",
            "-c",
            "#(nop) ",
            "CMD [\"nginx\" \"-g\" \"daemon off;\"]"
        ],
        "ArgsEscaped": true,
        "Image": "sha256:03243c86770b0f1423bf50f17d6cb7841d378d3029efb57c07e34163bf01dbc8",
        "Volumes": null,
        "WorkingDir": "",
        "Entrypoint": null,
        "OnBuild": [],
        "Labels": {
            "maintainer": "NGINX Docker Maintainers <docker-maint@nginx.com>"
        },
        "StopSignal": "SIGTERM"
    },
    "created": "2017-11-04T18:41:08.63323388Z",
    "docker_version": "17.06.2-ce",
    "history": [
        {
            "created": "2017-11-04T05:26:48.027090974Z",
            "created_by": "/bin/sh -c #(nop) ADD file:45233d6b5c9b91e9437065d3e7c332d1c4eb4bce8e1079a4c1af342c450abe67 in / "
        },
        {
            "created": "2017-11-04T05:26:48.324169027Z",
            "created_by": "/bin/sh -c #(nop)  CMD [\"bash\"]",
            "empty_layer": true
        },
        {
            "created": "2017-11-04T18:40:48.797395609Z",
            "created_by": "/bin/sh -c #(nop)  LABEL maintainer=NGINX Docker Maintainers <docker-maint@nginx.com>",
            "empty_layer": true
        },
        {
            "created": "2017-11-04T18:40:48.957138245Z",
            "created_by": "/bin/sh -c #(nop)  ENV NGINX_VERSION=1.13.6-1~stretch",
            "empty_layer": true
        },
        {
            "created": "2017-11-04T18:40:49.130229964Z",
            "created_by": "/bin/sh -c #(nop)  ENV NJS_VERSION=1.13.6.0.1.14-1~stretch",
            "empty_layer": true
        },
        {
            "created": "2017-11-04T18:41:07.163080323Z",
            "created_by": "/bin/sh -c set -x \t&& apt-get update \t&& apt-get install --no-install-recommends --no-install-suggests -y gnupg1 \t&& \tNGINX_GPGKEY=573BFD6B3D8FBC641079A6ABABF5BD827BD9BF62; \tfound=''; \tfor server in \t\tha.pool.sks-keyservers.net \t\thkp://keyserver.ubuntu.com:80 \t\thkp://p80.pool.sks-keyservers.net:80 \t\tpgp.mit.edu \t; do \t\techo \"Fetching GPG key $NGINX_GPGKEY from $server\"; \t\tapt-key adv --keyserver \"$server\" --keyserver-options timeout=10 --recv-keys \"$NGINX_GPGKEY\" && found=yes && break; \tdone; \ttest -z \"$found\" && echo >&2 \"error: failed to fetch GPG key $NGINX_GPGKEY\" && exit 1; \tapt-get remove --purge --auto-remove -y gnupg1 && rm -rf /var/lib/apt/lists/* \t&& dpkgArch=\"$(dpkg --print-architecture)\" \t&& nginxPackages=\" \t\tnginx=${NGINX_VERSION} \t\tnginx-module-xslt=${NGINX_VERSION} \t\tnginx-module-geoip=${NGINX_VERSION} \t\tnginx-module-image-filter=${NGINX_VERSION} \t\tnginx-module-njs=${NJS_VERSION} \t\" \t&& case \"$dpkgArch\" in \t\tamd64|i386) \t\t\techo \"deb http://nginx.org/packages/mainline/debian/ stretch nginx\" >> /etc/apt/sources.list \t\t\t&& apt-get update \t\t\t;; \t\t*) \t\t\techo \"deb-src http://nginx.org/packages/mainline/debian/ stretch nginx\" >> /etc/apt/sources.list \t\t\t\t\t\t&& tempDir=\"$(mktemp -d)\" \t\t\t&& chmod 777 \"$tempDir\" \t\t\t\t\t\t&& savedAptMark=\"$(apt-mark showmanual)\" \t\t\t\t\t\t&& apt-get update \t\t\t&& apt-get build-dep -y $nginxPackages \t\t\t&& ( \t\t\t\tcd \"$tempDir\" \t\t\t\t&& DEB_BUILD_OPTIONS=\"nocheck parallel=$(nproc)\" \t\t\t\t\tapt-get source --compile $nginxPackages \t\t\t) \t\t\t\t\t\t&& apt-mark showmanual | xargs apt-mark auto > /dev/null \t\t\t&& { [ -z \"$savedAptMark\" ] || apt-mark manual $savedAptMark; } \t\t\t\t\t\t&& ls -lAFh \"$tempDir\" \t\t\t&& ( cd \"$tempDir\" && dpkg-scanpackages . > Packages ) \t\t\t&& grep '^Package: ' \"$tempDir/Packages\" \t\t\t&& echo \"deb [ trusted=yes ] file://$tempDir ./\" > /etc/apt/sources.list.d/temp.list \t\t\t&& apt-get -o Acquire::GzipIndexes=false update \t\t\t;; \tesac \t\t&& apt-get install --no-install-recommends --no-install-suggests -y \t\t\t\t\t\t$nginxPackages \t\t\t\t\t\tgettext-base \t&& rm -rf /var/lib/apt/lists/* \t\t&& if [ -n \"$tempDir\" ]; then \t\tapt-get purge -y --auto-remove \t\t&& rm -rf \"$tempDir\" /etc/apt/sources.list.d/temp.list; \tfi"
        },
        {
            "created": "2017-11-04T18:41:08.042863814Z",
            "created_by": "/bin/sh -c ln -sf /dev/stdout /var/log/nginx/access.log \t&& ln -sf /dev/stderr /var/log/nginx/error.log"
        },
        {
            "created": "2017-11-04T18:41:08.250002271Z",
            "created_by": "/bin/sh -c #(nop)  EXPOSE 80/tcp",
            "empty_layer": true
        },
        {
            "created": "2017-11-04T18:41:08.445901099Z",
            "created_by": "/bin/sh -c #(nop)  STOPSIGNAL [SIGTERM]",
            "empty_layer": true
        },
        {
            "created": "2017-11-04T18:41:08.63323388Z",
            "created_by": "/bin/sh -c #(nop)  CMD [\"nginx\" \"-g\" \"daemon off;\"]",
            "empty_layer": true
        }
    ],
    "os": "linux",
    "rootfs": {
        "type": "layers",
        "diff_ids": [
            "sha256:cec7521cdf36a6d4ad8f2e92e41e3ac1b6fa6e05be07fa53cc84a63503bc5700",
            "sha256:bba7659ae2e77090e21f86e69d199aff4fcf9c71204fa3f3d5c89463c43eae7e",
            "sha256:f4cc3366d6a98b6cf5d047ad113ba76f67e4f5d20d12e37cd9a1f34ce0b421b9"
        ]
    }
}
*/
//给registry服务器发请求（里面包含配置文件的sha256和media type），拿到image的配置文件（Image Config）
//从仓库地址通过HTTP获取digest配置信息   (bs *blobs) Get
//返回的也就是 /var/lib/docker/image/devicemapper/imagedb/content/sha256/$imageid中的内容
func (p *v2Puller) pullSchema2Config(ctx context.Context, dgst digest.Digest) (configJSON []byte, err error) {
	//(r *repository) Blobs
	blobs := p.repo.Blobs(ctx) //构造blobs结构

	////从仓库地址通过HTTP获取digest配置信息   (bs *blobs) Get
	configJSON, err = blobs.Get(ctx, dgst)
	if err != nil {
		return nil, err
	}

	// Verify image config digest
	//(d Digest) Verifier() ，返回 hashVerifier 结构
	verifier := dgst.Verifier()
	if _, err := verifier.Write(configJSON); err != nil {
		return nil, err
	}
	if !verifier.Verified() {
		err := fmt.Errorf("image config verification failed for digest %s", dgst)
		logrus.Error(err)
		return nil, err
	}

	//fmt.Println(string(configJSON)) //yang test
	return configJSON, nil
}

// schema2ManifestDigest computes the manifest digest, and, if pulling by
// digest, ensures that it matches the requested digest.
//mfst对应 DeserializedManifest 结构, //返回HTTP请求的 manifest 包体内容
func schema2ManifestDigest(ref reference.Named, mfst distribution.Manifest) (digest.Digest, error) {
	//(m DeserializedManifest) Payload()
	_, canonical, err := mfst.Payload() //返回http请求的 manifest 包体内容中的mediatype和整个manifest内容
	if err != nil {
		return "", err
	}

	// If pull by digest, then verify the manifest digest.
	if digested, isDigested := ref.(reference.Canonical); isDigested {
		verifier := digested.Digest().Verifier()
		if _, err := verifier.Write(canonical); err != nil {
			return "", err
		}
		if !verifier.Verified() {
			err := fmt.Errorf("manifest verification failed for digest %s", digested.Digest())
			logrus.Error(err)
			return "", err
		}
		return digested.Digest(), nil
	}

	/*
	DEBU[0090] Trying to pull nginx from http://a9e61d46.m.daocloud.io/ v2
	yang test pullV2Repository  1111
	yang test  pullV2tag 1111
	yang test ... manifests.get url:http://a9e61d46.m.daocloud.io/v2/library/nginx/manifests/latest
	{
	   "schemaVersion": 2,
	   "mediaType": "application/vnd.docker.distribution.manifest.v2+json",
	   "config": {
	      "mediaType": "application/vnd.docker.container.image.v1+json",
	      "size": 5836,
	      "digest": "sha256:40960efd7b8f44ed5cafee61c189a8f4db39838848d41861898f56c29565266e"
	   },
	   "layers": [
	      {
		 "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
		 "size": 22492350,
		 "digest": "sha256:bc95e04b23c06ba1b9bf092d07d1493177b218e0340bd2ed49dac351c1e34313"
	      },
	      {
		 "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
		 "size": 21913353,
		 "digest": "sha256:a21d9ee25fc3dcef76028536e7191e44554a8088250d4c3ec884af23cef4f02a"
	      },
	      {
		 "mediaType": "application/vnd.docker.image.rootfs.diff.tar.gzip",
		 "size": 202,
		 "digest": "sha256:9bda7d5afd399f51550422c49172f8c9169fc3ffdef2748b13cfbf6467661ac5"
	      }
	   ]
	}
	*/
	//返回 manifest 包体内容
	return digest.FromBytes(canonical), nil
}

// allowV1Fallback checks if the error is a possible reason to fallback to v1
// (even if confirmedV2 has been set already), and if so, wraps the error in
// a fallbackError with confirmedV2 set to false. Otherwise, it returns the
// error unmodified.
func allowV1Fallback(err error) error {
	switch v := err.(type) {
	case errcode.Errors:
		if len(v) != 0 {
			if v0, ok := v[0].(errcode.Error); ok && shouldV2Fallback(v0) {
				return fallbackError{
					err:         err,
					confirmedV2: false,
					transportOK: true,
				}
			}
		}
	case errcode.Error:
		if shouldV2Fallback(v) {
			return fallbackError{
				err:         err,
				confirmedV2: false,
				transportOK: true,
			}
		}
	case *url.Error:
		if v.Err == auth.ErrNoBasicAuthCredentials {
			return fallbackError{err: err, confirmedV2: false}
		}
	}

	return err
}

func verifySchema1Manifest(signedManifest *schema1.SignedManifest, ref reference.Named) (m *schema1.Manifest, err error) {
	// If pull by digest, then verify the manifest digest. NOTE: It is
	// important to do this first, before any other content validation. If the
	// digest cannot be verified, don't even bother with those other things.
	if digested, isCanonical := ref.(reference.Canonical); isCanonical {
		verifier := digested.Digest().Verifier()
		if _, err := verifier.Write(signedManifest.Canonical); err != nil {
			return nil, err
		}
		if !verifier.Verified() {
			err := fmt.Errorf("image verification failed for digest %s", digested.Digest())
			logrus.Error(err)
			return nil, err
		}
	}
	m = &signedManifest.Manifest

	if m.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported schema version %d for %q", m.SchemaVersion, reference.FamiliarString(ref))
	}
	if len(m.FSLayers) != len(m.History) {
		return nil, fmt.Errorf("length of history not equal to number of layers for %q", reference.FamiliarString(ref))
	}
	if len(m.FSLayers) == 0 {
		return nil, fmt.Errorf("no FSLayers in manifest for %q", reference.FamiliarString(ref))
	}
	return m, nil
}

// fixManifestLayers removes repeated layers from the manifest and checks the
// correctness of the parent chain.
func fixManifestLayers(m *schema1.Manifest) error {
	imgs := make([]*image.V1Image, len(m.FSLayers))
	for i := range m.FSLayers {
		img := &image.V1Image{}

		if err := json.Unmarshal([]byte(m.History[i].V1Compatibility), img); err != nil {
			return err
		}

		imgs[i] = img
		if err := v1.ValidateID(img.ID); err != nil {
			return err
		}
	}

	if imgs[len(imgs)-1].Parent != "" && runtime.GOOS != "windows" {
		// Windows base layer can point to a base layer parent that is not in manifest.
		return errors.New("invalid parent ID in the base layer of the image")
	}

	// check general duplicates to error instead of a deadlock
	idmap := make(map[string]struct{})

	var lastID string
	for _, img := range imgs {
		// skip IDs that appear after each other, we handle those later
		if _, exists := idmap[img.ID]; img.ID != lastID && exists {
			return fmt.Errorf("ID %+v appears multiple times in manifest", img.ID)
		}
		lastID = img.ID
		idmap[lastID] = struct{}{}
	}

	// backwards loop so that we keep the remaining indexes after removing items
	for i := len(imgs) - 2; i >= 0; i-- {
		if imgs[i].ID == imgs[i+1].ID { // repeated ID. remove and continue
			m.FSLayers = append(m.FSLayers[:i], m.FSLayers[i+1:]...)
			m.History = append(m.History[:i], m.History[i+1:]...)
		} else if imgs[i].Parent != imgs[i+1].ID {
			return fmt.Errorf("Invalid parent ID. Expected %v, got %v.", imgs[i+1].ID, imgs[i].Parent)
		}
	}

	return nil
}

func createDownloadFile() (*os.File, error) {
	return ioutil.TempFile("", "GetImageBlob")
}
