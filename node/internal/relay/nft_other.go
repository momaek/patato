//go:build !linux

package relay

import (
	"fmt"
	"runtime"

	"github.com/momaek/patato/nodeapi"
)

// NFT 只在 Linux 上可用。
type NFT struct{}

func NewNFT() (*NFT, error) {
	return nil, fmt.Errorf("%w: %s 上没有 nftables", ErrUnavailable, runtime.GOOS)
}

func (*NFT) Name() string                               { return "nft" }
func (*NFT) Apply(fs []nodeapi.Forward) map[int64]error { return map[int64]error{} }
func (*NFT) Read() (map[int64]Counters, error)          { return map[int64]Counters{}, nil }
func (*NFT) Close() error                               { return nil }
func (*NFT) Remove() error                              { return nil }
