package pe

import "fmt"

// ScriptFile lets the ISO installer reuse the native Windows network preparation.
func ScriptFile(name string) ([]byte, error) {
	switch name {
	case "ndnic.js", "ndpe-report.js":
		return scripts.ReadFile("scripts/" + name)
	}
	return nil, fmt.Errorf("unknown PE script")
}
