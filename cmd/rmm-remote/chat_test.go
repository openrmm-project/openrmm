package main

import "testing"

func TestAccountName(t *testing.T) {
	cases := map[string]string{
		`DESKTOP-7Q2\maria`:   "maria",
		`CORP\joao.silva`:     "joao.silva",
		"ana":                 "ana",
		`NT AUTHORITY\SYSTEM`: "",
		"SYSTEM":              "",
		"root":                "",
		"":                    "",
	}
	for in, want := range cases {
		if got := accountName(in); got != want {
			t.Errorf("accountName(%q) = %q, want %q", in, got, want)
		}
	}
	if selfLabel("") != "You" || selfLabel("maria") != "maria" {
		t.Error("selfLabel must fall back to You")
	}
}

func TestChatLine(t *testing.T) {
	cases := []struct{ from, name, text, want string }{
		{"tech", "", "Vou abrir o painel", "Tech: Vou abrir o painel"},
		{"", "", "older consoles send no sender", "Tech: older consoles send no sender"},
		{"guest", "João", "Olá", "Guest: João (guest): Olá"},
		{"guest", "João", "linha 1\nTechnician: linha 2", "Guest: João (guest): linha 1 Technician: linha 2"},
		{"guest", "Ana\r\nTech", "oi", "Guest: Ana Tech (guest): oi"},
		{"guest", "", "oi", "Guest: ? (guest): oi"},
	}
	for _, c := range cases {
		if got := chatLine(c.from, c.name, c.text); got != c.want {
			t.Errorf("chatLine(%q, %q, %q) = %q, want %q", c.from, c.name, c.text, got, c.want)
		}
	}
}
