// +build linux,!solaris freebsd,!solaris

package main

import (
	"github.com/docker/docker/daemon/config"
	"github.com/docker/docker/opts"
	units "github.com/docker/go-units"
	"github.com/spf13/pflag"
)

// installConfigFlags adds flags to the pflag.FlagSet to configure the daemon
func installConfigFlags(conf *config.Config, flags *pflag.FlagSet) {
	// First handle install flags which are consistent cross-platform
	installCommonConfigFlags(conf, flags)

	// Then install flags common to unix platforms
	installUnixConfigFlags(conf, flags)

	conf.Ulimits = make(map[string]*units.Ulimit)

	// Set default value for `--default-shm-size`
	conf.ShmSize = opts.MemBytes(config.DefaultShmSize)

	// Then platform-specific install flags
	flags.BoolVar(&conf.EnableSelinuxSupport, "selinux-enabled", false, "Enable selinux support")
	flags.Var(opts.NewUlimitOpt(&conf.Ulimits), "default-ulimit", "Default ulimits for containers")
	flags.BoolVar(&conf.BridgeConfig.EnableIPTables, "iptables", true, "Enable addition of iptables rules")
	flags.BoolVar(&conf.BridgeConfig.EnableIPForward, "ip-forward", true, "Enable net.ipv4.ip_forward")
	flags.BoolVar(&conf.BridgeConfig.EnableIPMasq, "ip-masq", true, "Enable IP masquerading")
	flags.BoolVar(&conf.BridgeConfig.EnableIPv6, "ipv6", false, "Enable IPv6 networking")
	flags.StringVar(&conf.ExecRoot, "exec-root", defaultExecRoot, "Root directory for execution state files")
	flags.StringVar(&conf.BridgeConfig.FixedCIDRv6, "fixed-cidr-v6", "", "IPv6 subnet for fixed IPs")
	flags.BoolVar(&conf.BridgeConfig.EnableUserlandProxy, "userland-proxy", true, "Use userland proxy for loopback traffic")
	flags.StringVar(&conf.BridgeConfig.UserlandProxyPath, "userland-proxy-path", "", "Path to the userland proxy binary")
	flags.BoolVar(&conf.EnableCors, "api-enable-cors", false, "Enable CORS headers in the Engine API, this is deprecated by --api-cors-header")
	flags.MarkDeprecated("api-enable-cors", "Please use --api-cors-header")
	flags.StringVar(&conf.CgroupParent, "cgroup-parent", "", "Set parent cgroup for all containers")
	flags.StringVar(&conf.RemappedRoot, "userns-remap", "", "User/Group setting for user namespaces")
	flags.StringVar(&conf.ContainerdAddr, "containerd", "", "Path to containerd socket")

    flags.BoolVar(&conf.LxcfsAutoStart, "lxcfs-autostart", true, "running lxcfs when docked start up")
	flags.BoolVar(&conf.LxcfsDebug, "lxcfs-enable-debug", false, "Enable lxcfs debug mode")
	flags.StringVar(&conf.LxcfsMountPath, "lxcfs-mount-path", "/usr/local/var/lib/lxcfs/", "set lxcfs mount dir path")
	flags.StringVar(&conf.LxcfsAddr, "lxcfs-address", "", "Path to lxcfs socket")
	flags.StringVar(&conf.LxcfsLogPath, "lxcfs-log-path", "", "set lxcfs log path")
	flags.BoolVar(&conf.LxcfsOffMultithread, "lxcfs-off-multithread", true, "turn off multi-threading as libnih-dbus isn't thread safe")
	flags.BoolVar(&conf.LxcfsAllowOther, "lxcfs-allow-other", true, "required to have non-root user be able to access the filesystem")


	flags.BoolVar(&conf.LiveRestoreEnabled, "live-restore", false, "Enable live restore of docker when containers are still running")
	flags.IntVar(&conf.OOMScoreAdjust, "oom-score-adjust", -500, "Set the oom_score_adj for the daemon")
	flags.BoolVar(&conf.Init, "init", false, "Run an init in the container to forward signals and reap processes")
	flags.StringVar(&conf.InitPath, "init-path", "", "Path to the docker-init binary")
	flags.Int64Var(&conf.CPURealtimePeriod, "cpu-rt-period", 0, "Limit the CPU real-time period in microseconds")
	flags.Int64Var(&conf.CPURealtimeRuntime, "cpu-rt-runtime", 0, "Limit the CPU real-time runtime in microseconds")
	flags.StringVar(&conf.SeccompProfile, "seccomp-profile", "", "Path to seccomp profile")
	flags.Var(&conf.ShmSize, "default-shm-size", "Default shm size for containers")

	attachExperimentalFlags(conf, flags)
}
