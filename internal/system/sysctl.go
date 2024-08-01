package system

import (
	_ "embed"
	"fmt"
	"os/exec"
	"path"

	"github.com/aws/eks-hybrid/internal/api"
	"github.com/aws/eks-hybrid/internal/util"
)

const (
	sysctlAspectName      = "sysctl"
	sysctlConfDir         = "/etc/sysctl.d"
	nodeadmSysctlConfFile = "99-nodeadm.conf"
	nodeadmSysctlFilePerm = 0644
)

var (
	//go:embed _assets/99-sysctl.conf
	sysctlConfFileData    string
	nodeadmSysctlConfPath = path.Join(sysctlConfDir, nodeadmSysctlConfFile)
)

type sysctlAspect struct{}

var _ SystemAspect = &sysctlAspect{}

func NewSysctlAspect() *sysctlAspect {
	return &sysctlAspect{}
}
func (s *sysctlAspect) Name() string {
	return sysctlAspectName
}

func (s *sysctlAspect) Setup(cfg *api.NodeConfig) error {
	if err := writeSysctlConfig(); err != nil {
		return err
	}
	return reloadSysctl()
}

func writeSysctlConfig() error {
	return util.WriteFileWithDir(nodeadmSysctlConfPath, []byte(sysctlConfFileData), nodeadmSysctlFilePerm)
}

func reloadSysctl() error {
	reloadCmd := exec.Command(sysctlAspectName, "--system")
	out, err := reloadCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("running sysctl reload command: %s, error: %v", out, err)
	}
	return nil
}