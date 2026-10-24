package domain

import "time"

const TaskTypeInstallImage TaskType = "install_image"

// InstallSession retains the work disk across setup reboots; unfinished candidates stay outside the catalogue.
type InstallSession struct {
	ID            string
	MAC           string
	IP            string
	Netmask       string
	Gateway       string
	DNS1          string
	Username      string
	Password      string
	Token         string
	Attempt       int
	BootID        string
	BootStartedAt time.Time // Paired with BootID; progress reports must not move this timestamp.
	OSType        OSType
	EditionIndex  int
	// Language 是所选版本的默认语言，写进无人值守应答文件。
	Language           string
	SizeBytes          int64
	Target             string
	Portal             string
	LUN                int
	ImageID            string
	Name               string
	PoolID             string
	ServerID           string
	SourcePath         string
	SourceSHA256       string
	Distribution       string
	Version            string
	Architecture       string
	Phase              string
	HWID               string
	Firmware           string
	BusLoc             string
	MediaDir           string
	TaskID             string
	Error              string
	Message            string
	Progress           int
	CandidateImage     Image
	CandidateConfig    Config
	CandidateReduction Reduction
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DisconnectedAt     *time.Time
	LastReportAt       *time.Time
}

func (s InstallSession) Active() bool {
	return s.Phase != "ready" && s.Phase != "cancelled"
}
