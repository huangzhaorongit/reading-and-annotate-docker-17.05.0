package distribution

import (
	"fmt"
	"mime"

	"github.com/docker/distribution/context"
	"github.com/opencontainers/go-digest"
)

// Manifest represents a registry object specifying a set of
// references and an optional target
//distribution\manifest\schema2\manifest.go 中的 type Manifest struct 结构 中实现这些接口
/*
如果HTTP ctHeader 头部中的resp.Header.Get("Content-Type")为"application/json",则执行 schema1Func，返回 SignedManifest，Descriptor
如果头部字段Content-Type内容为"application/vnd.docker.distribution.manifest.v2+json"对应V2，则执行 schema2Func，返回 DeserializedManifest，Descriptor
如果头部字段Content-Type内容为"application/vnd.docker.distribution.manifest.list.v2+json"则对应manifestlist，则执行 manifestListFunc，返回 DeserializedManifestList，Descriptor
*/
//SignedManifest DeserializedManifest DeserializedManifestList 三个结构都会实现该接口
type Manifest interface {
	// References returns a list of objects which make up this manifest.
	// A reference is anything which can be represented by a
	// distribution.Descriptor. These can consist of layers, resources or other
	// manifests.
	//
	// While no particular order is required, implementations should return
	// them from highest to lowest priority. For example, one might want to
	// return the base layer before the top layer.
	References() []Descriptor

	// Payload provides the serialized format of the manifest, in addition to
	// the media type.
	Payload() (mediaType string, payload []byte, err error)
}

// ManifestBuilder creates a manifest allowing one to include dependencies.
// Instances can be obtained from a version-specific manifest package.  Manifest
// specific data is passed into the function which creates the builder.
type ManifestBuilder interface {
	// Build creates the manifest from his builder.
	Build(ctx context.Context) (Manifest, error)

	// References returns a list of objects which have been added to this
	// builder. The dependencies are returned in the order they were added,
	// which should be from base to head.
	References() []Descriptor

	// AppendReference includes the given object in the manifest after any
	// existing dependencies. If the add fails, such as when adding an
	// unsupported dependency, an error may be returned.
	//
	// The destination of the reference is dependent on the manifest type and
	// the dependency type.
	AppendReference(dependency Describable) error
}

// ManifestService describes operations on image manifests.
//type manifests struct 实现这些接口
type ManifestService interface {
	// Exists returns true if the manifest exists.
	Exists(ctx context.Context, dgst digest.Digest) (bool, error)

	// Get retrieves the manifest specified by the given digest
	Get(ctx context.Context, dgst digest.Digest, options ...ManifestServiceOption) (Manifest, error)

	// Put creates or updates the given manifest returning the manifest digest
	Put(ctx context.Context, manifest Manifest, options ...ManifestServiceOption) (digest.Digest, error)

	// Delete removes the manifest specified by the given digest. Deleting
	// a manifest that doesn't exist will return ErrManifestNotFound
	Delete(ctx context.Context, dgst digest.Digest) error
}

// ManifestEnumerator enables iterating over manifests
type ManifestEnumerator interface {
	// Enumerate calls ingester for each manifest.
	Enumerate(ctx context.Context, ingester func(digest.Digest) error) error
}

// Describable is an interface for descriptors
type Describable interface {
	Descriptor() Descriptor
}

// ManifestMediaTypes returns the supported media types for manifests.
func ManifestMediaTypes() (mediaTypes []string) {
	for t := range mappings {
		if t != "" {
			mediaTypes = append(mediaTypes, t)
		}
	}
	return
}

// UnmarshalFunc implements manifest unmarshalling a given MediaType
type UnmarshalFunc func([]byte) (Manifest, Descriptor, error)

//RegisterManifestSchema 中注册填充   func:对应 schema1Func 或者 schema2Func 或者 manifestListFunc，
// 这些func在 UnmarshalManifest 中执行
//V1 RegisterManifestSchema("application/json", schema1Func)   V2  RegisterManifestSchema(MediaTypeManifest, schema2Func)
//RegisterManifestSchema(MediaTypeManifestList, manifestListFunc)  MediaTypeManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"

// MediaTypeManifest = "application/vnd.docker.distribution.manifest.v2+json"  map中的string对应的就是HTTP头部中的resp.Header.Get("Content-Type")
//如果头部字段Content-Type内容为"application/vnd.docker.distribution.manifest.v2+json"对应V2，"application/json"对应V1
//如果头部字段Content-Type内容为"application/vnd.docker.distribution.manifest.list.v2+json"则对应manifestlist
var mappings = make(map[string]UnmarshalFunc, 0)

// UnmarshalManifest looks up manifest unmarshal functions based on
// MediaType
//ctHeader 来源为 mt := resp.Header.Get("Content-Type")，即包体内容type，如html txt等等  p为HTTP包体内容

/*
如果HTTP ctHeader 头部中的resp.Header.Get("Content-Type")为"application/json",则执行 schema1Func，返回 SignedManifest，Descriptor
如果头部字段Content-Type内容为"application/vnd.docker.distribution.manifest.v2+json"对应V2，则执行 schema2Func，返回 DeserializedManifest，Descriptor
如果头部字段Content-Type内容为"application/vnd.docker.distribution.manifest.list.v2+json"则对应manifestlist，则执行 manifestListFunc，返回 DeserializedManifestList，Descriptor
*/ //(ms *manifests) Get 中调用执行，把仓库获取的manifest内容反序列化存储到Manifest结构
func UnmarshalManifest(ctHeader string, p []byte) (Manifest, Descriptor, error) {
	// Need to look up by the actual media type, not the raw contents of
	// the header. Strip semicolons and anything following them.
	var mediaType string
	if ctHeader != "" {
		var err error
		mediaType, _, err = mime.ParseMediaType(ctHeader)
		if err != nil {
			return nil, Descriptor{}, err
		}
	}

	//对应 schema1Func 或者 schema2Func 或者 manifestListFunc
	unmarshalFunc, ok := mappings[mediaType]
	if !ok {
		unmarshalFunc, ok = mappings[""]
		if !ok {
			return nil, Descriptor{}, fmt.Errorf("unsupported manifest media type and no default available: %s", mediaType)
		}
	}

//
//func:对应 schema1Func 或者 schema2Func 或者 manifestListFunc， 执行func,这几个函数只能全文搜索才能搜索到
/*
如果HTTP ctHeader 头部中的resp.Header.Get("Content-Type")为"application/json",则执行 schema1Func，返回 SignedManifest，Descriptor
如果头部字段Content-Type内容为"application/vnd.docker.distribution.manifest.v2+json"对应V2，则执行 schema2Func，返回 DeserializedManifest，Descriptor
如果头部字段Content-Type内容为"application/vnd.docker.distribution.manifest.list.v2+json"则对应manifestlist，则执行 manifestListFunc，返回 DeserializedManifestList，Descriptor
*/
	return unmarshalFunc(p)
}

// RegisterManifestSchema registers an UnmarshalFunc for a given schema type.  This
// should be called from specific
//u 对应 schema1Func 或者 schema2Func 或者 manifestListFunc，
//V1 RegisterManifestSchema("application/json", schema1Func)   V2  RegisterManifestSchema(MediaTypeManifest, schema2Func) MediaTypeManifest = "application/vnd.docker.distribution.manifest.v2+json"
//RegisterManifestSchema(MediaTypeManifestList, manifestListFunc)  MediaTypeManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
func RegisterManifestSchema(mediaType string, u UnmarshalFunc) error {
	if _, ok := mappings[mediaType]; ok {
		return fmt.Errorf("manifest media type registration would overwrite existing: %s", mediaType)
	}
	mappings[mediaType] = u
	return nil
}
