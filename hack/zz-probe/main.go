package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/windsorcli/terraform-provider-hyperv/internal/connection"
)

func open(maxShells int) connection.Connection {
	c, err := connection.NewWinRM(connection.WinRMOptions{
		Host: os.Getenv("HYPERV_HOST"), Port: 5986, Username: os.Getenv("HYPERV_USERNAME"),
		Password: []byte(os.Getenv("HYPERV_PASSWORD")), UseHTTPS: true, Insecure: true, MaxShells: maxShells,
	})
	if err != nil {
		panic(err)
	}
	if err := c.Open(context.Background()); err != nil {
		panic(err)
	}
	return c
}

func main() {
	ctx := context.Background()
	c := open(3)
	r, err := c.RunScript(ctx, `(Get-Item WSMan:\localhost\Shell\MaxShellsPerUser).Value; (Get-Item WSMan:\localhost\Plugin\microsoft.powershell\Quotas\MaxShellsPerUser -EA SilentlyContinue).Value`, nil)
	fmt.Printf("MaxShellsPerUser: %q err=%v\n", string(r.Stdout), err)
	c.Close()
	for _, n := range os.Args[1:] {
		ms, _ := strconv.Atoi(n)
		c := open(ms)
		var fail atomic.Int32
		var first atomic.Value
		var wg sync.WaitGroup
		start := time.Now()
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := c.RunScript(ctx, `Start-Sleep -Seconds 3; 'ok'`, nil)
				if err != nil {
					fail.Add(1)
					first.CompareAndSwap(nil, err.Error())
				}
			}()
		}
		wg.Wait()
		fmt.Printf("max_shells=%d: 10 concurrent calls, %d failed, %s; first error: %v\n", ms, fail.Load(), time.Since(start).Round(time.Second), first.Load())
		c.Close()
	}
}
