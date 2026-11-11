package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/p1ngu/rmm/internal/scriptvars"
)

func varEnv(shell string, vars map[string]string) ([]string, error) {
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
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("invalid value for script variable %s", name)
		}
		env = append(env, name+"="+value)
	}
	sort.Strings(env)
	return env, nil
}
