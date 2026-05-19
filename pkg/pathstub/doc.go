// Package pathstub builds the read-only FUSE view of remote PATH
// executables. It receives path enumeration results and a remote file
// opener from mproxy-agent and backend file clients, and feeds namespace
// PATH lookup plus broker path mapping with stubs that point back to the
// real remote binaries.
package pathstub
