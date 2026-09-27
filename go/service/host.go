package service

import (
	"context"
	"errors"
	"os/user"
	"strconv"
	"sync"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// MaxFrameBytes bounds one request or reply frame. A Snapshot page of 256
// manifests with their entries and holds fits well inside it.
const MaxFrameBytes = 8 << 20

// callBudget bounds one call, including Observe's longest wait.
const callBudget = time.Duration(MaxWaitMs)*time.Millisecond + 10*time.Second

// Host serves one Source on the shared identity-bound native transport: a
// named pipe restricted to this account on Windows, a socket in a
// per-account directory elsewhere. Every call rechecks the bound peer: an
// identity the transport could not obtain is the error code
// caller_unavailable, one below the Program proof is identity_required, and
// a bound peer of another account reads forbidden. Nothing here registers the
// endpoint with the platform or the runtime.
type Host struct {
	source   *Source
	listener listen.Listener
	owner    string
	ctx      context.Context
	cancel   context.CancelFunc
	once     sync.Once
	workers  sync.WaitGroup
	slots    chan struct{}

	lifecycle sync.Mutex
	serving   bool
	// OnError, assigned before Serve, hears failed calls.
	OnError func(error)
}

// Listen binds endpoint for source. The endpoint is explicit; Listen performs
// no discovery and refuses an endpoint another listener holds.
func Listen(endpoint string, source *Source) (*Host, error) {
	if source == nil {
		return nil, errors.New("inventoryd: source required")
	}
	owner, err := user.Current()
	if err != nil {
		return nil, err
	}
	if owner.Uid == "" {
		return nil, errors.New("inventoryd: service principal unavailable")
	}
	l, err := listen.ListenFramed(endpoint, listen.Program)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Host{source: source, listener: listen.Sessions(l, listen.SessionOptions{MaxSessions: 16}), owner: owner.Uid, ctx: ctx, cancel: cancel, slots: make(chan struct{}, 16)}, nil
}

// Close stops accepting and ends waits in progress.
func (h *Host) Close() error {
	var err error
	h.once.Do(func() { h.cancel(); err = h.listener.Close() })
	return err
}

// Serve answers calls until ctx ends or Close.
func (h *Host) Serve(ctx context.Context) error {
	h.lifecycle.Lock()
	if h.serving {
		h.lifecycle.Unlock()
		return errors.New("inventoryd: host already served")
	}
	h.serving = true
	h.lifecycle.Unlock()
	//unchecked: Close is idempotent (sync.Once); this async cancellation callback has no caller to report the error to, and Serve's own deferred Close below is the same no-op afterward
	stop := context.AfterFunc(ctx, func() { h.Close() })
	defer stop()
	defer h.workers.Wait()
	defer h.Close()
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			if ctx.Err() != nil || h.ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case h.slots <- struct{}{}:
		default:
			//unchecked: dropping a connection because the worker slots are full; nothing here can act on a failed close of the connection it is already refusing
			conn.Close()
			continue
		}
		h.workers.Add(1)
		go func() {
			defer h.workers.Done()
			defer func() { <-h.slots }()
			defer conn.Close()
			callCtx, cancel := context.WithTimeout(h.ctx, callBudget)
			defer cancel()
			call, err := listen.ReceiveFramed(callCtx, conn, listen.Program, MaxFrameBytes)
			if call != nil {
				defer call.Close()
			}
			if err == nil {
				dispatcher := content.InventorySourceDispatcher{Handler: &receiver{host: h, call: call, ctx: callCtx}}
				var reply []byte
				reply, err = dispatcher.ExchangeFrame(call.Frame)
				if err == nil {
					err = call.Reply(reply)
				}
			}
			if err != nil && h.OnError != nil && h.ctx.Err() == nil {
				h.OnError(err)
			}
		}()
	}
}

type receiver struct {
	host *Host
	call *listen.FramedCall
	ctx  context.Context
}

// admit returns nil for the service's own account, forbidden for another
// bound account, and a ServiceError when no identity is established.
func (r *receiver) admit() (forbidden bool, err error) {
	peer, err := r.call.Peer()
	if err != nil {
		return false, &content.ServiceError{Code: content.ServiceErrorCodeCallerUnavailable}
	}
	return r.host.admits(peer)
}

func (h *Host) admits(peer *identity.Peer) (forbidden bool, err error) {
	u, err := peer.User.AtLeast(listen.Program.User)
	if err != nil {
		return false, &content.ServiceError{Code: content.ServiceErrorCodeIdentityRequired}
	}
	principal := ""
	switch u.Kind {
	case "windows":
		principal = u.SID
	case "posix":
		principal = strconv.Itoa(u.UID)
	}
	return principal == "" || principal != h.owner, nil
}

func (r *receiver) Describe() (content.SourceDescription, error) {
	if forbidden, err := r.admit(); err != nil || forbidden {
		return content.SourceDescription{Outcome: content.SourceDescriptionOutcomeForbidden, Stores: []content.Store{}, Schemes: []string{}, Capabilities: []string{}}, err
	}
	return r.host.source.Describe()
}

func (r *receiver) Snapshot(continuation string, limit int64) (content.SourcePage, error) {
	if forbidden, err := r.admit(); err != nil || forbidden {
		return refusal(content.SourcePageOutcomeForbidden), err
	}
	return r.host.source.Snapshot(continuation, limit)
}

func (r *receiver) Observe(cursor string, maxChanges, waitMs int64) (content.SourcePage, error) {
	if forbidden, err := r.admit(); err != nil || forbidden {
		return refusal(content.SourcePageOutcomeForbidden), err
	}
	return r.host.source.ObserveContext(r.call.WaitContext(), cursor, maxChanges, waitMs)
}

func (r *receiver) Verify(target string) (content.SourcePage, error) {
	if forbidden, err := r.admit(); err != nil || forbidden {
		return refusal(content.SourcePageOutcomeForbidden), err
	}
	return r.host.source.Verify(target)
}

func (r *receiver) Remove(target string) (content.RemoveResult, error) {
	if forbidden, err := r.admit(); err != nil || forbidden {
		return content.RemoveResult{Outcome: content.RemoveOutcomeForbidden, Holders: []content.Holder{}}, err
	}
	return r.host.source.Remove(target)
}
