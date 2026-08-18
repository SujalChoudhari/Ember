package main

import "flag"

func main() {
	baseURL := flag.String("base", "http://127.0.0.1:8080", "Ember HTTP endpoint")
	bearerToken := flag.String("token", "", "bearer token (prefer an external mode-0600 auth file in scripts)")
	flag.Parse()
	runCommand(*baseURL, *bearerToken, flag.Args())
}
