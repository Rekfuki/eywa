package cache

import (
	"github.com/patrickmn/go-cache"
	log "github.com/sirupsen/logrus"

	"eywa/gateway/types"
)

type Config struct {
}

type Client struct {
	functions *cache.Cache
}

// Setup initialises a new cache
func Setup(conf *Config) *Client {
	return &Client{
		functions: cache.New(cache.NoExpiration, cache.NoExpiration),
	}
}

func (c *Client) LookupFunction(name string) *types.Function {
	functionInterface, found := c.functions.Get(name)
	if !found {
		log.Debugf("Function %q not found", name)
		return nil
	}

	if fn, ok := functionInterface.(*types.Function); ok {
		return fn
	} else {
		log.Errorf("Cache returned not a Function type: %T", functionInterface)
		return nil
	}
}