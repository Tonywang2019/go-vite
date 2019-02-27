package filters

import (
	"context"
	"fmt"
	"github.com/pkg/errors"
	"github.com/vitelabs/go-vite/common/types"
	"github.com/vitelabs/go-vite/ledger"
	"github.com/vitelabs/go-vite/log15"
	"github.com/vitelabs/go-vite/rpc"
	"github.com/vitelabs/go-vite/rpcapi/api"
	"github.com/vitelabs/go-vite/vite"
	"sync"
	"time"
)

var (
	deadline = 5 * time.Minute // consider a filter inactive if it has not been polled for within deadline
)

type filter struct {
	typ             FilterType
	deadline        *time.Timer
	param           filterParam
	s               *RpcSubscription
	blocks          []*AccountBlockMsg
	confirmedBlocks []*ConfirmedAccountBlockMsg
	logs            []*LogsMsg
	confirmedLogs   []*LogsMsg
}

type SubscribeApi struct {
	vite        *vite.Vite
	log         log15.Logger
	filterMap   map[rpc.ID]*filter
	filterMapMu sync.Mutex
	eventSystem *EventSystem
}

func NewSubscribeApi(vite *vite.Vite) *SubscribeApi {
	s := &SubscribeApi{
		vite:        vite,
		log:         log15.New("module", "rpc_api/subscribe_api"),
		filterMap:   make(map[rpc.ID]*filter),
		eventSystem: Es,
	}
	go s.timeoutLoop()
	return s
}

func (s *SubscribeApi) timeoutLoop() {
	fmt.Println("start filter timeout loop")
	// delete timeout filters every 5 minutes
	ticker := time.NewTicker(5 * time.Minute)
	for {
		<-ticker.C
		s.filterMapMu.Lock()
		for id, f := range s.filterMap {
			select {
			case <-f.deadline.C:
				f.s.Unsubscribe()
				delete(s.filterMap, id)
			default:
				continue
			}
		}
		s.filterMapMu.Unlock()
	}
}

type Range struct {
	FromHeight string `json:"fromHeight"`
	ToHeight   string `json:"toHeight"`
}

func (r *Range) toHeightRange() (*heightRange, error) {
	if r != nil {
		fromHeight, err := api.StringToUint64(r.FromHeight)
		if err != nil {
			return nil, err
		}
		toHeight, err := api.StringToUint64(r.ToHeight)
		if err != nil {
			return nil, err
		}
		return &heightRange{fromHeight, toHeight}, nil
	}
	return nil, nil
}

type RpcFilterParam struct {
	SnapshotRange *Range            `json:"snapshotRange"`
	AddrRange     map[string]*Range `json:"addrRange"`
	Topics        [][]types.Hash    `json:"topics"`
	AccountHash   *types.Hash       `json:"accountHash"`
	SnapshotHash  *types.Hash       `json:"snapshotHash"`
}

func (p *RpcFilterParam) toFilterParam() (*filterParam, error) {
	snapshotRange, err := p.SnapshotRange.toHeightRange()
	if err != nil {
		return nil, err
	}
	var addrRange map[types.Address]heightRange
	if len(p.AddrRange) == 0 {
		return nil, errors.New("addrRange is nil")
	}
	addrRange = make(map[types.Address]heightRange, len(p.AddrRange))
	for hexAddr, r := range p.AddrRange {
		hr, err := r.toHeightRange()
		if err != nil {
			return nil, err
		}
		if hr == nil {
			hr = &heightRange{0, 0}
		}
		addr, err := types.HexToAddress(hexAddr)
		if err != nil {
			return nil, err
		}
		addrRange[addr] = *hr
	}
	target := &filterParam{
		snapshotRange: snapshotRange,
		addrRange:     addrRange,
		topics:        p.Topics,
		accountHash:   p.AccountHash,
		snapshotHash:  p.SnapshotHash,
	}
	return target, nil
}

type AccountBlockMsg struct {
	Hash    types.Hash `json:"hash"`
	Removed bool       `json:"removed"`
}

type ConfirmedAccountBlockMsg struct {
	HashList     []types.Hash `json:"hashList"`
	SnapshotHash types.Hash   `json:"snapshotHash"`
	Removed      bool         `json:"removed"`
}

type LogsMsg struct {
	Log               *ledger.VmLog  `json:"log"`
	AccountBlockHash  types.Hash     `json:"accountBlockHash"`
	SnapshotBlockHash *types.Hash    `json:"snapshotBlockHash"`
	Addr              *types.Address `json:"addr"`
	Removed           bool           `json:"removed"`
}

func (s *SubscribeApi) NewAccountBlocksFilter() (rpc.ID, error) {
	fmt.Println("new account blocks filter start")
	var (
		acCh  = make(chan []*AccountBlockMsg)
		acSub = s.eventSystem.SubscribeAccountBlocks(acCh)
	)

	s.filterMapMu.Lock()
	s.filterMap[acSub.ID] = &filter{typ: acSub.sub.typ, deadline: time.NewTimer(deadline), s: acSub}
	s.filterMapMu.Unlock()

	go func() {
		for {
			select {
			case ac := <-acCh:
				s.filterMapMu.Lock()
				if f, found := s.filterMap[acSub.ID]; found {
					f.blocks = append(f.blocks, ac...)
				}
				s.filterMapMu.Unlock()
			case <-acSub.Err():
				s.filterMapMu.Lock()
				delete(s.filterMap, acSub.ID)
				s.filterMapMu.Unlock()
				return
			}
		}
	}()

	return acSub.ID, nil
}

func (s *SubscribeApi) NewConfirmedAccountBlocksFilter() (rpc.ID, error) {
	fmt.Println("new confirmed account blocks filter start")
	var (
		acCh  = make(chan []*ConfirmedAccountBlockMsg)
		acSub = s.eventSystem.SubscribeConfirmedAccountBlocks(acCh)
	)

	s.filterMapMu.Lock()
	s.filterMap[acSub.ID] = &filter{typ: acSub.sub.typ, deadline: time.NewTimer(deadline), s: acSub}
	s.filterMapMu.Unlock()

	go func() {
		for {
			select {
			case ac := <-acCh:
				s.filterMapMu.Lock()
				if f, found := s.filterMap[acSub.ID]; found {
					f.confirmedBlocks = append(f.confirmedBlocks, ac...)
				}
				s.filterMapMu.Unlock()
			case <-acSub.Err():
				s.filterMapMu.Lock()
				delete(s.filterMap, acSub.ID)
				s.filterMapMu.Unlock()
				return
			}
		}
	}()

	return acSub.ID, nil
}

func (s *SubscribeApi) NewLogsFilter(param RpcFilterParam) (rpc.ID, error) {
	fmt.Println("new logs filter start")
	p, err := param.toFilterParam()
	if err != nil {
		return "", err
	}
	var (
		logsCh  = make(chan []*LogsMsg)
		logsSub = s.eventSystem.SubscribeLogs(p, logsCh)
	)

	s.filterMapMu.Lock()
	s.filterMap[logsSub.ID] = &filter{typ: logsSub.sub.typ, deadline: time.NewTimer(deadline), s: logsSub}
	s.filterMapMu.Unlock()

	go func() {
		for {
			select {
			case l := <-logsCh:
				s.filterMapMu.Lock()
				if f, found := s.filterMap[logsSub.ID]; found {
					f.logs = append(f.logs, l...)
				}
				s.filterMapMu.Unlock()
			case <-logsSub.Err():
				s.filterMapMu.Lock()
				delete(s.filterMap, logsSub.ID)
				s.filterMapMu.Unlock()
				return
			}
		}
	}()

	return logsSub.ID, nil
}

func (s *SubscribeApi) NewConfirmedLogsFilter(param RpcFilterParam) (rpc.ID, error) {
	fmt.Println("new confirmed logs filter start")
	p, err := param.toFilterParam()
	if err != nil {
		return "", err
	}
	var (
		logsCh  = make(chan []*LogsMsg)
		logsSub = s.eventSystem.SubscribeConfirmedLogs(p, logsCh)
	)

	s.filterMapMu.Lock()
	s.filterMap[logsSub.ID] = &filter{typ: logsSub.sub.typ, deadline: time.NewTimer(deadline), s: logsSub}
	s.filterMapMu.Unlock()

	go func() {
		for {
			select {
			case l := <-logsCh:
				s.filterMapMu.Lock()
				if f, found := s.filterMap[logsSub.ID]; found {
					f.confirmedLogs = append(f.confirmedLogs, l...)
				}
				s.filterMapMu.Unlock()
			case <-logsSub.Err():
				s.filterMapMu.Lock()
				delete(s.filterMap, logsSub.ID)
				s.filterMapMu.Unlock()
				return
			}
		}
	}()

	return logsSub.ID, nil
}

func (s *SubscribeApi) UninstallFilter(id rpc.ID) bool {
	s.filterMapMu.Lock()
	f, found := s.filterMap[id]
	if found {
		delete(s.filterMap, id)
	}
	s.filterMapMu.Unlock()
	if found {
		f.s.Unsubscribe()
	}
	return found
}

func (s *SubscribeApi) GetFilterChanges(id rpc.ID) (interface{}, error) {
	s.filterMapMu.Lock()
	defer s.filterMapMu.Unlock()

	if f, found := s.filterMap[id]; found {
		if !f.deadline.Stop() {
			<-f.deadline.C
		}
		f.deadline.Reset(deadline)

		switch f.typ {
		case AccountBlocksSubscription:
			blocks := f.blocks
			f.blocks = nil
			return blocks, nil
		case LogsSubscription:
			logs := f.logs
			f.logs = nil
			return logs, nil
		}
	}

	return nil, errors.New("filter not found")
}

func (s *SubscribeApi) NewAccountBlocks(ctx context.Context) (*rpc.Subscription, error) {
	notifier, supported := rpc.NotifierFromContext(ctx)
	if !supported {
		return &rpc.Subscription{}, rpc.ErrNotificationsUnsupported
	}
	rpcSub := notifier.CreateSubscription()

	go func() {
		accountBlockHashCh := make(chan []*AccountBlockMsg, 128)
		acSub := s.eventSystem.SubscribeAccountBlocks(accountBlockHashCh)
		for {
			select {
			case h := <-accountBlockHashCh:
				notifier.Notify(rpcSub.ID, h)
			case <-rpcSub.Err():
				acSub.Unsubscribe()
				return
			case <-notifier.Closed():
				acSub.Unsubscribe()
				return
			}
		}
	}()

	return rpcSub, nil
}

func (s *SubscribeApi) NewConfirmedAccountBlocks(ctx context.Context) (*rpc.Subscription, error) {
	notifier, supported := rpc.NotifierFromContext(ctx)
	if !supported {
		return &rpc.Subscription{}, rpc.ErrNotificationsUnsupported
	}
	rpcSub := notifier.CreateSubscription()

	go func() {
		acMsg := make(chan []*ConfirmedAccountBlockMsg, 128)
		sub := s.eventSystem.SubscribeConfirmedAccountBlocks(acMsg)

		for {
			select {
			case msg := <-acMsg:
				notifier.Notify(rpcSub.ID, msg)
			case <-rpcSub.Err():
				sub.Unsubscribe()
				return
			case <-notifier.Closed():
				sub.Unsubscribe()
				return
			}
		}
	}()
	return rpcSub, nil
}

func (s *SubscribeApi) NewLogs(ctx context.Context, param RpcFilterParam) (*rpc.Subscription, error) {
	p, err := param.toFilterParam()
	if err != nil {
		return nil, err
	}

	notifier, supported := rpc.NotifierFromContext(ctx)
	if !supported {
		return &rpc.Subscription{}, rpc.ErrNotificationsUnsupported
	}
	rpcSub := notifier.CreateSubscription()

	go func() {
		logsMsg := make(chan []*LogsMsg, 128)
		sub := s.eventSystem.SubscribeLogs(p, logsMsg)

		for {
			select {
			case msg := <-logsMsg:
				notifier.Notify(rpcSub.ID, msg)
			case <-rpcSub.Err():
				sub.Unsubscribe()
				return
			case <-notifier.Closed():
				sub.Unsubscribe()
				return
			}
		}
	}()
	return rpcSub, nil
}

func (s *SubscribeApi) NewConfirmedLogs(ctx context.Context, param RpcFilterParam) (*rpc.Subscription, error) {
	p, err := param.toFilterParam()
	if err != nil {
		return nil, err
	}

	notifier, supported := rpc.NotifierFromContext(ctx)
	if !supported {
		return &rpc.Subscription{}, rpc.ErrNotificationsUnsupported
	}
	rpcSub := notifier.CreateSubscription()

	go func() {
		logsMsg := make(chan []*LogsMsg, 128)
		sub := s.eventSystem.SubscribeConfirmedLogs(p, logsMsg)

		for {
			select {
			case msg := <-logsMsg:
				notifier.Notify(rpcSub.ID, msg)
			case <-rpcSub.Err():
				sub.Unsubscribe()
				return
			case <-notifier.Closed():
				sub.Unsubscribe()
				return
			}
		}
	}()
	return rpcSub, nil
}
