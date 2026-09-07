package state

// A PID domain is the set of processes whose PIDs mean something to each other:
// ask the operating system whether PID 4711 is running and the answer is only
// about this domain. Nothing this program writes can name one. An identifier
// kept in a file of ours is a property of that file, and a home directory
// mounted on two machines hands both of them one identifier — after which each
// reads the other's live PIDs as dead and takes its locks. So the name comes
// from the operating system: the machine it is running on, narrowed to the PID
// namespace where the kernel has them.
//
// pidDomain is the read. It is a variable so a test can stand in for another
// machine, another namespace, or a system that will not say; only a test
// replaces it.
var pidDomain = readPIDDomain

// currentPIDDomain names the PID domain this process is in, or nothing when the
// system will not say. Nothing is lost for creation: only reclamation consults
// it, so a process without one takes locks as usual and reclaims none — and the
// records it writes name no domain and are reclaimed by nobody either, which is
// the safe direction for both.
func currentPIDDomain() string {
	domain, err := pidDomain()
	if err != nil {
		return ""
	}
	return domain
}
