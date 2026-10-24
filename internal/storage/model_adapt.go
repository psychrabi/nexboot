package storage

// ModelAdaptReq adds offline driver preparation to the normal single-target clone request.
type ModelAdaptReq struct {
	ClientReq
	HWID      string
	DriverDir string
}
