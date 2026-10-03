package geminilive

import "errors"

// Zero keeps provider defaults. Calibration never uses a zero-silence VAD.
type VADConfig struct {
	SilenceDurationMS int
	PrefixPaddingMS   int
	EndSensitivity    string
	Hybrid            bool
}

func (v VADConfig) Validate() error {
	if v.SilenceDurationMS != 0 && (v.SilenceDurationMS < 500 || v.SilenceDurationMS > 2000) {
		return errors.New("unsafe VAD silence threshold")
	}
	if v.PrefixPaddingMS < 0 || v.PrefixPaddingMS > 500 {
		return errors.New("invalid VAD prefix padding")
	}
	if v.EndSensitivity != "" && v.EndSensitivity != "END_SENSITIVITY_LOW" && v.EndSensitivity != "END_SENSITIVITY_HIGH" {
		return errors.New("invalid VAD end sensitivity")
	}
	return nil
}
