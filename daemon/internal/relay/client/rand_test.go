package client

import (
	"math/rand"
	"time"
)

func newRand() *rand.Rand { return rand.New(rand.NewSource(time.Now().UnixNano())) }
