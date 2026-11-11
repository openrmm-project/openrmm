package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/p1ngu/rmm/internal/scriptvars"
)

// varEnv builds the script variables for a job against this process's own
// environment: a variable never replaces one the device already defines.
func varEnv(shell string, vars map[string]string) ([]string, error) {
	return varEnvOver(shell, vars, os.Environ())
}

// varEnvOver refuses a user variable whose name, ignoring case, is already in
// base. The child process environment is base followed by these entries, and
// the last occurrence wins (case-insensitively on Windows), so without this a
// value could replace PATH-like settings the denylist does not name, such as
// ProgramFiles or PSModulePath, on the devices of a scoped manager.
func varEnvOver(shell string, vars map[string]string, base []string) ([]string, error) {
	existing := map[string]bool{}
	for _, entry := range base {
		if name, _, ok := strings.Cut(entry, "="); ok && name != "" {
			existing[strings.ToUpper(name)] = true
		}
	}
	if err := scriptvars.CheckShell(shell, len(vars)); err != nil {
		return nil, err
	}
	if err := scriptvars.ValidEnvironmentSize(vars); err != nil {
		return nil, err
	}
	if len(vars) > 38 {
		return nil, fmt.Errorf("too many script variables")
	}
	builtins := scriptvars.Builtins(scriptvars.Device{})
	var env []string
	seen := map[string]bool{}
	for name, value := range vars {
		canonical := strings.ToUpper(name)
		if seen[canonical] {
			return nil, fmt.Errorf("duplicate environment variable name ignoring case")
		}
		seen[canonical] = true
		if _, builtin := builtins[name]; !builtin {
			if err := scriptvars.ValidUserName(name); err != nil {
				return nil, err
			}
			if existing[canonical] {
				return nil, fmt.Errorf("script variable %s would replace a variable this device already defines; rename it", name)
			}
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("invalid value for script variable %s", name)
		}
		env = append(env, name+"="+value)
	}
	sort.Strings(env)
	return env, nil
}
