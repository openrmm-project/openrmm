package main

import (
	"fmt"
	"github.com/p1ngu/rmm/internal/scriptvars"
	"sort"
	"strings"
)

func varEnv(shell string, vars map[string]string) ([]string, error) {
	if len(vars) > 38 {
		return nil, fmt.Errorf("too many script variables")
	}
	builtins := scriptvars.Builtins(scriptvars.Device{})
	var env []string
	for name, value := range vars {
		if _, builtin := builtins[name]; !builtin {
			if err := scriptvars.ValidUserName(name); err != nil {
				return nil, err
			}
		}
		if len(value) > scriptvars.MaxValueLen || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("invalid value for script variable %s", name)
		}
		env = append(env, name+"="+value)
	}
	sort.Strings(env)
	return env, nil
}
