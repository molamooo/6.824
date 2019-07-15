package raft

//
// this is an outline of the API that raft must expose to
// the service (or tester). see comments below for
// each of these functions for more details.
//
// rf = Make(...)
//   create a new Raft server.
// rf.Start(command interface{}) (index, term, isleader)
//   start agreement on a new log entry
// rf.GetState() (term, isLeader)
//   ask a Raft for its current term, and whether it thinks it is leader
// ApplyMsg
//   each time a new entry is committed to the log, each Raft peer
//   should send an ApplyMsg to the service (or tester)
//   in the same server.
//

import "sync"
import "labrpc"
import "time"
import "math/rand"
import "sort"
import "fmt"

import "bytes"
import "labgob"

// Roles
const (
	Follower = iota
	Candidate
	Leader
)

const leaderCheckDelta int64 = 1e8 // 0.1s
const leaderStepDownDelta int64 = 3e8
const electionTimeoutBase int64 = 2e8
const electionTimeoutRange int64 = 6e8
const heartBeatDelta int64 = 1e8
const applyCheckDelta int64 = 2e7
const maxAppendEntries int = 100

func myDebug(other ...interface{}) {
	/*
	fmt.Print(time.Now().String()[14:25], " ")
	fmt.Println(other...) 
	// */
	fmt.Print("")
}

// ApplyMsg :
// as each Raft peer becomes aware that successive log entries are
// committed, the peer should send an ApplyMsg to the service (or
// tester) on the same server, via the applyCh passed to Make(). set
// CommandValid to true to indicate that the ApplyMsg contains a newly
// committed log entry.
//
// in Lab 3 you'll want to send other kinds of messages (e.g.,
// snapshots) on the applyCh; at that point you can add fields to
// ApplyMsg, but set CommandValid to false for these other uses.
//
type ApplyMsg struct {
	CommandValid bool
	Command      interface{}
	CommandIndex int
}

type Entry struct {
	Data interface{}
	Term int
}

// Raft :
// A Go object implementing a single Raft peer.
//
type Raft struct {
	mu        sync.Mutex          // Lock to protect shared access to this peer's state
	peers     []*labrpc.ClientEnd // RPC end points of all peers
	persister *Persister          // Object to hold this peer's persisted state
	me        int                 // this peer's index into peers[]

	// Your data here (2A, 2B, 2C).
	// Look at the paper's Figure 2 for a description of what
	// state a Raft server must maintain.
	
	// persistent status, TODO: remember to persist it
	currentTerm int // what term it believes in
	votedFor    int // candidate index it voted to in this term, -1 for never voted
	log         []Entry
	// volatile status
	committedIndex int // 0 initial
	lastApplied    int // 0 initial
	// volatile status of leader, reinitialize when newly elected
	nextIndex      []int
	matchIndex     []int
	
	lastLeaderTS   int64 // last time when leader called
	role           int // follower, candidate, leader
	leader         int // the leader index that this one believes in, init with -1
	terminate      bool

	appendNoticeChan chan interface {}

	// applyChan      chan ApplyMsg
}

// GetState :
// return currentTerm and whether this server
// believes it is the leader.
func (rf *Raft) GetState() (int, bool) {

	// Your code here (2A).
	rf.mu.Lock()
	term, role := rf.currentTerm, rf.role == Leader
	rf.mu.Unlock()
	return term, role
}


//
// save Raft's persistent state to stable storage,
// where it can later be retrieved after a crash and restart.
// see paper's Figure 2 for a description of what should be persistent.
//
// Warn: must call this inside lock!
func (rf *Raft) persist() {
	// Your code here (2C).
	wbuf := new(bytes.Buffer)
	encoder := labgob.NewEncoder(wbuf)
	encoder.Encode(rf.currentTerm)
	encoder.Encode(rf.votedFor)
	encoder.Encode(rf.log)
	data := wbuf.Bytes()
	rf.persister.SaveRaftState(data)
}


//
// restore previously persisted state.
//
func (rf *Raft) readPersist(data []byte) {
	if data == nil || len(data) < 1 { // bootstrap without any state?
		return
	}
	// Your code here (2C).
	rbuf := bytes.NewBuffer(data)
	decoder := labgob.NewDecoder(rbuf)
	var currentTerm int
	var votedFor int
	var log []Entry
	if decoder.Decode(&currentTerm) != nil ||
	   decoder.Decode(&votedFor) != nil || 
	   decoder.Decode(&log) != nil {
	  // error...
	} else {
	  rf.currentTerm = currentTerm
	  rf.votedFor = votedFor
	  rf.log = log
	}
}

func findCommitIndex(matchIndex []int) int {
	// the leader self's matchIndex is always 0
	locallist := make([]int, len(matchIndex))
	for i:= 0; i < len(matchIndex); i++ {
		locallist[i] = matchIndex[i]
	}
	// sort(locallist)
	sort.Ints(locallist)
	return locallist[(len(matchIndex)+1)/2]
	// 2: 1
	// 3: 2
	// 4: 2
	// 5: 3
	// 6: 3
}




// RequestVoteArgs :
// example RequestVote RPC arguments structure.
// field names must start with capital letters!
//
type RequestVoteArgs struct {
	// Your data here (2A, 2B).
	Term            int
	CandidateId     int
	LastLogIndex    int
	LastLogTerm     int
}

// RequestVoteReply :
// example RequestVote RPC reply structure.
// field names must start with capital letters!
//
type RequestVoteReply struct {
	// Your data here (2A).
	Term            int
	VoteGranted     bool
}


// RequestVote :
// example RequestVote RPC handler.
//
func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	// Your code here (2A, 2B).
	rf.mu.Lock()
	if rf.currentTerm > args.Term {
		myDebug(rf.me, "<-", args.CandidateId, " :reject voting due to local new term ", 
						rf.currentTerm, " and old remote ", args.Term)
		// I'm newer
		reply.Term = rf.currentTerm
		reply.VoteGranted = false
	} else if rf.currentTerm < args.Term {
		// grant vote
		myDebug(rf.me, "<-", args.CandidateId, " :recv newer term reqVote, move term  ", 
						rf.currentTerm, " to ", args.Term)
		rf.currentTerm = args.Term
		rf.role = Follower
		rf.leader = -1
		rf.votedFor = -1
		// do not start election 
		// rf.lastLeaderTS = time.Now().UnixNano()
		
		localLastIndex := len(rf.log)-1
		localLastTerm := rf.log[len(rf.log)-1].Term
		if localLastTerm > args.LastLogTerm || 
				(localLastTerm == args.LastLogTerm && localLastIndex > args.LastLogIndex) {
			// reject
			reply.Term = rf.currentTerm
			reply.VoteGranted = false
			myDebug(rf.me, "<-", args.CandidateId, " :reject voting due to local logs (", 
							localLastTerm, localLastIndex, ")>(", args.LastLogTerm, args.LastLogIndex, ")")
		} else {
			myDebug(rf.me, "<-", args.CandidateId, " :grant voting due to remote logs (", 
							args.LastLogTerm, args.LastLogIndex, ")>=(", localLastTerm, localLastIndex, ")")
			// accept
			rf.votedFor = args.CandidateId
			reply.Term = rf.currentTerm
			reply.VoteGranted = true
		}
		rf.persist()
	} else if (rf.votedFor == -1 || rf.votedFor == args.CandidateId) && rf.role == Follower {
		myDebug(rf.me, "<-", args.CandidateId, " :recv reqVote, t=", args.Term)
		// grant vote
		localLastIndex := len(rf.log)-1
		localLastTerm := rf.log[len(rf.log)-1].Term
		if localLastTerm > args.LastLogTerm || 
				(localLastTerm == args.LastLogTerm && localLastIndex > args.LastLogIndex) {
			// reject
			myDebug(rf.me, "<-", args.CandidateId, " :reject voting due to local logs (", 
							localLastTerm, localLastIndex, ")>(", args.LastLogTerm, args.LastLogIndex, ")")
			reply.Term = rf.currentTerm
			reply.VoteGranted = false
		} else {
			// accept
			rf.votedFor = args.CandidateId
			rf.persist()
			reply.Term = rf.currentTerm
			rf.lastLeaderTS = time.Now().UnixNano()
			reply.VoteGranted = true
			myDebug(rf.me, "<-", args.CandidateId, " :grant voting due to remote logs (", 
							args.LastLogTerm, args.LastLogIndex, ")>=(", localLastTerm, localLastIndex, ")")
		}
	} else {
		myDebug(rf.me, "<-", args.CandidateId, " reject voting due to voted or not follower")
		reply.Term = rf.currentTerm
		reply.VoteGranted = false
	}
	rf.mu.Unlock()
}

type AppendEntriesArgs struct {
	Term         int
	LeaderId     int // what does this do?
	PrevLogIndex int
	PrevLogTerm  int
	Entries      []Entry
	LeaderCommit int // leader's commit Index
}

type AppendEntriesReply struct {
	Term         int
	Success      bool
}

func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	// this should set local timestamp
	

	// when to update:
	// rf.term = args.term 
	// rf.term = args.term and rf.role = candidate:
	//    rf.role = follower, rf.leader = args.id
	// rf.term < args.term
	//    rf.role = follower, rf.leader = args.id, rf.term = args.term

	// when not to update:
	// rf.term = args.term, rf.role = follower, rf.leader = args.id(must if not -1)

	rf.mu.Lock()

	if rf.currentTerm > args.Term {
		myDebug(rf.me, "<-", args.LeaderId, ": recved heart beat(", args.Term, ") is stale(", 
						rf.currentTerm, ")")
		reply.Term = rf.currentTerm
		reply.Success = false
		rf.mu.Unlock()
		return
	}
	rf.lastLeaderTS = time.Now().UnixNano()

	reply.Term = args.Term

	if rf.currentTerm == args.Term && rf.role == Follower && rf.leader != -1 {
		// nothing to modify here
		if (rf.leader != args.LeaderId) {
			panic("Bug detected: different leader for same term")
		}
	} else if rf.currentTerm < args.Term {
		// catch up the new term
		rf.votedFor = -1
		myDebug(rf.me, "<-", args.LeaderId, " :append, move term from ", 
						rf.currentTerm, " to ", args.Term)
		rf.currentTerm = args.Term
		rf.role = Follower
		rf.leader = args.LeaderId
		rf.lastLeaderTS = time.Now().UnixNano()
		rf.persist()
	} else {
		// maybe need to learn the current term's leader, give up election
		rf.lastLeaderTS = time.Now().UnixNano()
		rf.role = Follower
		rf.leader = args.LeaderId
		myDebug(rf.me, "<-", args.LeaderId, " :append term checked.", 
						" may give up election in the same term=", args.Term)
	}

	if args.PrevLogIndex >= len(rf.log) {
		// i have shorter log
		myDebug(rf.me, "<-", args.LeaderId, " :reject append due to missing logs, ", 
						"(pI, localLen)=", args.PrevLogIndex, len(rf.log))
		reply.Success = false
	} else if rf.log[args.PrevLogIndex].Term != args.PrevLogTerm {
		myDebug(rf.me, "<-", args.LeaderId, " :reject append due to diff prev term,",
						" (pI, pT, localTerm)=", args.PrevLogIndex, args.PrevLogTerm, rf.log[args.PrevLogIndex].Term)
		reply.Success = false
	} else {
		if len(args.Entries) != 0 {
			myDebug(rf.me, "<-", args.LeaderId, " :accept append some entries")
		}
		// now update local entries
		nextEntryToPut := 0
		nextIndexToPut := args.PrevLogIndex + 1
		logModified := false
		// if the nextIndexToPut is already in rf.log, check the term
		for ; nextIndexToPut < len(rf.log) && nextEntryToPut < len(args.Entries); nextIndexToPut++ {
			if rf.log[nextIndexToPut].Term != args.Entries[nextEntryToPut].Term {
				rf.log = rf.log[:nextIndexToPut]
				logModified = true
				if rf.committedIndex >= nextIndexToPut {
					myDebug(rf.me, " bug? a commited index is cutted due to log term mismatch ", rf.committedIndex, nextIndexToPut)
				}
				break
			}
			nextEntryToPut++
		}
		for ; nextEntryToPut < len(args.Entries); nextEntryToPut++ {
			rf.log = append(rf.log, args.Entries[nextEntryToPut])
			logModified = true
		}
		reply.Success = true
		if args.LeaderCommit > rf.committedIndex {
			if args.LeaderCommit < len(rf.log)-1 {
				myDebug(rf.me, " catch up committedIndex to ", args.LeaderCommit)
				rf.committedIndex = args.LeaderCommit
			} else {
				myDebug(rf.me, " catch up committedIndex to ", len(rf.log)-1)
				rf.committedIndex = len(rf.log)-1
			}
		}
		if logModified {
			rf.persist()
		}
	}

	rf.mu.Unlock()
}

//
// example code to send a RequestVote RPC to a server.
// server is the index of the target server in rf.peers[].
// expects RPC arguments in args.
// fills in *reply with RPC reply, so caller should
// pass &reply.
// the types of the args and reply passed to Call() must be
// the same as the types of the arguments declared in the
// handler function (including whether they are pointers).
//
// The labrpc package simulates a lossy network, in which servers
// may be unreachable, and in which requests and replies may be lost.
// Call() sends a request and waits for a reply. If a reply arrives
// within a timeout interval, Call() returns true; otherwise
// Call() returns false. Thus Call() may not return for a while.
// A false return can be caused by a dead server, a live server that
// can't be reached, a lost request, or a lost reply.
//
// Call() is guaranteed to return (perhaps after a delay) *except* if the
// handler function on the server side does not return.  Thus there
// is no need to implement your own timeouts around Call().
//
// look at the comments in ../labrpc/labrpc.go for more details.
//
// if you're having trouble getting RPC to work, check that you've
// capitalized all field names in structs passed over RPC, and
// that the caller passes the address of the reply struct with &, not
// the struct itself.
//
func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	ok := rf.peers[server].Call("Raft.RequestVote", args, reply)
	return ok
}


//
// the service using Raft (e.g. a k/v server) wants to start
// agreement on the next command to be appended to Raft's log. if this
// server isn't the leader, returns false. otherwise start the
// agreement and return immediately. there is no guarantee that this
// command will ever be committed to the Raft log, since the leader
// may fail or lose an election. even if the Raft instance has been killed,
// this function should return gracefully.
//
// the first return value is the index that the command will appear at
// if it's ever committed. the second return value is the current
// term. the third return value is true if this server believes it is
// the leader.
//
func (rf *Raft) Start(command interface{}) (int, int, bool) {

	// Your code here (2B).
	rf.mu.Lock()
	
	if rf.role != Leader{
		rf.mu.Unlock()
		return -1, -1, false
	}

	index := len(rf.log)
	term := rf.currentTerm

	rf.log = append(rf.log, Entry{command, term})
	rf.persist()
	myDebug(rf.me, " append a cmd from client, now len(log)=", len(rf.log))
	rf.mu.Unlock()
	go func() {
		select {
		case rf.appendNoticeChan <- nil:
		default:
		}
	} ()

	return index, term, true
}

// Kill :
// the tester calls Kill() when a Raft instance won't
// be needed again. you are not required to do anything
// in Kill(), but it might be convenient to (for example)
// turn off debug output from this instance.
//
func (rf *Raft) Kill() {
	// Your code here, if desired.
	rf.mu.Lock()
	rf.terminate = true
	rf.mu.Unlock()
}

func tryNotice(Chan chan interface {}) {
	select {
	case Chan <- nil:
	default:
	}
}

func (rf *Raft) leaderProdecure() {
	// go rf.heartBeatProcedure()
	me := rf.me
	appendNoticeChanList := make([]chan interface{}, len(rf.peers))
	for i := 0; i < len(rf.peers); i++ {
		if me == i {
			continue
		}
		appendNoticeChanList[i] = make(chan interface{})

		go func(i int) {
			// this routine constantly check the nextIndex, matchIndex
			nextIndexDecStep := 1
			for {
				<- appendNoticeChanList[i]
				rf.mu.Lock()
				if rf.terminate {
					rf.mu.Unlock()
					return
				}
				if rf.role != Leader {
					rf.mu.Unlock()
					return
				}
				go func() {
					nextIndex := rf.nextIndex[i]
					args := AppendEntriesArgs{}
					args.Term = rf.currentTerm
					args.LeaderId = me
					args.PrevLogIndex = nextIndex-1
					args.PrevLogTerm = rf.log[nextIndex-1].Term
					if len(rf.log) - 1 < rf.nextIndex[i] {
						// myDebug(rf.me, "sending out heart beat to ", i)
						args.Entries = nil
					} else {
						myDebug(rf.me, "->", i, ": meaningful append(t, pT, pI)=", 
										args.Term, args.PrevLogTerm, args.PrevLogIndex)
						if (nextIndex + maxAppendEntries > len(rf.log)) {
							args.Entries = rf.log[nextIndex :]
						} else {
							args.Entries = rf.log[nextIndex : nextIndex + maxAppendEntries]
						}
					}
					args.LeaderCommit = rf.committedIndex
					rf.mu.Unlock()

					reply := AppendEntriesReply{}
					// go func() {
					ok := rf.peers[i].Call("Raft.AppendEntries", &args, &reply)
					// } ()
					if !ok {
						return
					}
					rf.mu.Lock()
					if reply.Term > args.Term || rf.currentTerm > args.Term {
						if reply.Term > rf.currentTerm {
							rf.currentTerm = reply.Term
							rf.votedFor = -1
							rf.leader = -1
							rf.role = Follower
							// rf.lastLeaderTS = time.Now().UnixNano()
							rf.persist()
						}
						rf.mu.Unlock()
						return
					} else if reply.Success {
						nextIndexDecStep = 1
						if args.PrevLogIndex == rf.nextIndex[i]-1 {
							rf.nextIndex[i]+= len(args.Entries)
						}
						if rf.nextIndex[i] - 1 > rf.matchIndex[i] {
							rf.matchIndex[i] = rf.nextIndex[i] -1
						}
						ci := findCommitIndex(rf.matchIndex)
						if ci > rf.committedIndex {
							if rf.log[ci].Term >= rf.currentTerm {
								myDebug(rf.me, " move committedIndex to ", ci)
								rf.committedIndex = ci
							}
						}
					} else {
						if args.PrevLogIndex == rf.nextIndex[i]-1 {
							orig := rf.nextIndex[i]
							rf.nextIndex[i] -= nextIndexDecStep
							nextIndexDecStep *= 2
							if rf.nextIndex[i] <= 0 {
								rf.nextIndex[i] = 1
							}
							myDebug(rf.me, " dec nextIndex of ", i, " from ", orig, " to ", rf.nextIndex[i])
						}
					}
					rf.mu.Unlock()
				} ()

			}
			

		} (i)
	}
	go func() {
		// a routine that constantly try to notify leader to send out appendEntries
		for {
			rf.mu.Lock()
			role := rf.role
			terminate := rf.terminate
			rf.mu.Unlock()
			if terminate {
				return
			}
			if role != Leader {
				tryNotice(rf.appendNoticeChan)
				return
			}
			tryNotice(rf.appendNoticeChan)
			time.Sleep(time.Duration(heartBeatDelta))
		}
	} ()
	go func() {
		// a routine that read from prev routine and distribute notifications to routine 
		// for each other peer
		for {
			rf.mu.Lock()
			role := rf.role
			terminate := rf.terminate
			rf.mu.Unlock()
			if terminate {
				return
			}
			if role != Leader {
				for i:=0; i < len(rf.peers); i++ {
					if i == me {
						continue
					}
					tryNotice(appendNoticeChanList[i])
				}
				return
			}
			<-rf.appendNoticeChan
			for i:=0; i < len(rf.peers); i++ {
				if i == me {
					continue
				}
				tryNotice(appendNoticeChanList[i])
			}
		}
	} ()

}


func (rf *Raft) electionProcedure() {
	for {
		rf.mu.Lock()
		if rf.terminate {
			rf.mu.Unlock()
			return
		}
		if rf.role != Candidate {
			myDebug(rf.me, " terminate election due to term changed by some routine else")
			rf.mu.Unlock()
			return
		}
		rf.currentTerm ++
		thisTerm := rf.currentTerm
		rf.votedFor = rf.me
		rf.persist()
		requestVoteArgs := RequestVoteArgs{}
		requestVoteArgs.LastLogIndex = len(rf.log)-1
		requestVoteArgs.LastLogTerm = rf.log[requestVoteArgs.LastLogIndex].Term
		rf.mu.Unlock()
		n := len(rf.peers)
		requestVoteArgs.Term = thisTerm
		requestVoteArgs.CandidateId = rf.me
		myDebug(rf.me, " electing in term ", thisTerm)
		votesOfThisTerm := make(chan int, n-1)
		rejectedVotesByLogOfThisTerm := make(chan int, 1)
		rejectedVotesByLogOfThisTerm <- 0
		
		for i:=0; i < n; i++ {
			if i == rf.me {
				continue
			}
			requestVoteReply := &RequestVoteReply{}
			go func(i int) {
				myDebug(rf.me, "->", i, " :sending vote ")
				ok := rf.sendRequestVote(
					i, &requestVoteArgs, requestVoteReply)
				// should do some callback to count majority
				if !ok {
					return
				}
				if requestVoteReply.VoteGranted{
					myDebug(rf.me, "->", i, " :recv vote grant of term", thisTerm, ", put to chan")
					votesOfThisTerm <- i
				} else {
					if requestVoteReply.Term > thisTerm {
						rf.mu.Lock()
						if requestVoteReply.Term > rf.currentTerm {
							myDebug(rf.me, " is stale, move from candidate in ", rf.currentTerm, 
											" to follower in ", requestVoteReply.Term)
							rf.role = Follower
							rf.currentTerm = requestVoteReply.Term
							rf.votedFor = -1
							rf.lastLeaderTS = time.Now().UnixNano()
							rf.leader = -1
							rf.persist()
						}
						rf.mu.Unlock()
					} else {
						orig := <-rejectedVotesByLogOfThisTerm
						rejectedVotesByLogOfThisTerm <- orig+1
					}
				}
			} (i)
		}
		go func() {
			rand.Seed(time.Now().UnixNano())
			time.Sleep(time.Duration(rand.Int63n(electionTimeoutRange) + electionTimeoutBase))
			myDebug(rf.me, " election timelimit, election count chan <- -1, t=", thisTerm)
			votesOfThisTerm <- -1
		} ()

		voteReceived := 0
		for {
			i := <-votesOfThisTerm
			if i == -1 {
				myDebug(rf.me, " election term ", thisTerm, " timeout, reelecting in next term")
				rejected := <- rejectedVotesByLogOfThisTerm
				rejectedVotesByLogOfThisTerm <- rejected
				if rejected > n/2 {
					// myDebug(rf.me, " majority rejected me in this election term ", thisTerm)
					// rf.mu.Lock()
					// rf.lastLeaderTS = time.Now().UnixNano()
					// rf.mu.Unlock()
					// return
				}
				// election failed, reelection
				break
			}

			voteReceived ++
			myDebug(rf.me, "->", i, " recv granted vote of term", thisTerm)
			if voteReceived >= n / 2 {
				rf.mu.Lock()
				// election succeed
				if rf.currentTerm > thisTerm {
					myDebug(rf.me, "->", i, " recv grant vote in term ", thisTerm, ", but I've moved on")
					// election succeed, but this server has already stale and move to next term
					rf.mu.Unlock()
					return
				}
				myDebug(rf.me, " has f+1 votes and becomes leader of term ", thisTerm)
				rf.role = Leader
				// start heart beat procedure
				rf.leader = rf.me
				rf.matchIndex = make([]int, len(rf.peers))
				rf.nextIndex = make([]int, len(rf.peers))
				for i := 0; i < len(rf.peers); i++ {
					rf.nextIndex[i] = len(rf.log)
				}
				rf.mu.Unlock()
				go func (rf *Raft) {
					rf.leaderProdecure()
				} (rf)
				return
			}
		}
	}
}

// Make :
// the service or tester wants to create a Raft server. the ports
// of all the Raft servers (including this one) are in peers[]. this
// server's port is peers[me]. all the servers' peers[] arrays
// have the same order. persister is a place for this server to
// save its persistent state, and also initially holds the most
// recent saved state, if any. applyCh is a channel on which the
// tester or service expects Raft to send ApplyMsg messages.
// Make() must return quickly, so it should start goroutines
// for any long-running work.
//
func Make(peers []*labrpc.ClientEnd, me int,
	persister *Persister, applyCh chan ApplyMsg) *Raft {
	rf := &Raft{}
	rf.peers = peers
	rf.persister = persister
	rf.me = me
	// rf.applyChan = applyCh

	// Your initialization code here (2A, 2B, 2C).
	rf.role = Follower
	rf.currentTerm = 0
	rf.votedFor = -1
	rf.log = make([]Entry, 1)
	rf.log[0] = Entry{nil, 0}
	rf.committedIndex = 0
	rf.lastApplied = 0
	rf.lastLeaderTS = 0
	rf.leader = -1
	rf.terminate = false
	rf.appendNoticeChan = make(chan interface{})

	// initialize from state persisted before a crash
	rf.readPersist(persister.ReadRaftState())

	go func () {
		for {
			time.Sleep(time.Duration(leaderCheckDelta))
			rf.mu.Lock()
			role := rf.role
			terminate := rf.terminate
			rf.mu.Unlock()
			if terminate {
				return
			}
			if role != Follower {
				continue
			}
			now := time.Now().UnixNano()
			if now - rf.lastLeaderTS > leaderStepDownDelta {
				myDebug(rf.me, " thinks leader is down, start election")
				// start election
				rf.role = Candidate
				rf.electionProcedure()
			}
		}
	} ()

	go func () {
		for {
			if rf.terminate {
				return
			}
			rf.mu.Lock()
			if rf.committedIndex > rf.lastApplied {
				applyMsg := ApplyMsg{}
				rf.lastApplied ++
				applyMsg.CommandIndex = rf.lastApplied
				applyMsg.Command = rf.log[applyMsg.CommandIndex].Data
				rf.mu.Unlock()
				applyMsg.CommandValid = true
				applyCh <- applyMsg
			} else {
				rf.mu.Unlock()
				time.Sleep(time.Duration(applyCheckDelta))
			}
		}
	} ()


	return rf
}
