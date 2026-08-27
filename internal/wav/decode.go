package wav

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Read reads any audio file as 16 kHz mono, which is what every model here
// takes. A WAV is read directly and anything else goes through ffmpeg.
//
// Worth shelling out for, because a recording arrives as whatever the thing
// that made it writes: a phone gives .m4a, a screen recorder gives .mkv, and
// the workshop this was built for arrived as .ac3 and .opus. Converting by hand
// first is a step to get wrong -- the wrong rate, or a stereo file kept in
// stereo -- and it is a step ffmpeg does not need help with.
func Read(path string) ([]float32, int, error) {
	if strings.EqualFold(filepath.Ext(path), ".wav") {
		return ReadWAV(path)
	}
	// Raw floats rather than a WAV, since a WAV written to a pipe carries a
	// length nobody knew yet in its header.
	cmd := exec.Command("ffmpeg", "-v", "error", "-i", path,
		"-map", "0:a:0", "-ac", "1", "-ar", strconv.Itoa(SampleRate), "-f", "f32le", "-")
	var out, errs bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errs
	if err := cmd.Run(); err != nil {
		if _, missing := err.(*exec.Error); missing {
			return nil, 0, fmt.Errorf("%s is not a WAV, and ffmpeg is not installed to convert it", path)
		}
		return nil, 0, fmt.Errorf("ffmpeg %s: %v: %s", path, err, strings.TrimSpace(errs.String()))
	}
	raw := out.Bytes()
	samples := make([]float32, len(raw)/4)
	for i := range samples {
		samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[4*i:]))
	}
	if len(samples) == 0 {
		return nil, 0, fmt.Errorf("%s has no audio", path)
	}
	return samples, SampleRate, nil
}
