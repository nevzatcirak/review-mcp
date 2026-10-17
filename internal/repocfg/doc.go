// Package repocfg reads the repository config of X-40 and merges its allowed
// settings into a run's configuration.
//
// A repository can tune how review-mcp reviews it with a file at its root
// (repo_config.path, default .review-mcp.toml). The file is read at the pull
// request's base revision, the target branch, and never at the head revision,
// so a pull request cannot change the settings it is reviewed with. Only an
// allow-list of harmless keys is read (allow.go); everything else is ignored
// and named in a note. Nothing in the file can hold or reveal a secret, or
// change a host, an endpoint, a model or a limit that protects the user.
//
// Values of the file are never quoted in notes or logs: key names may appear,
// sanitised and capped, and nothing else of the file does.
package repocfg
