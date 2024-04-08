//go:build consumer
// +build consumer

package grpc

import (
	"context"
	"fmt"
	"github.com/pact-foundation/pact-go/v2/examples/grpc/primary"
	"github.com/pact-foundation/pact-go/v2/log"
	message "github.com/pact-foundation/pact-go/v2/message/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetRectangleSuccess(t *testing.T) {
	p, _ := message.NewSynchronousPact(message.Config{
		Consumer: "grpcconsumer",
		Provider: "grpcprovider",
		PactDir:  filepath.ToSlash(fmt.Sprintf("%s/../pacts", dir)),
	})
	require.NoError(t, log.SetLogLevel("DEBUG"))

	dir, _ := os.Getwd()
	path := fmt.Sprintf("%s/primary/primary.proto", dir)

	grpcInteraction := `{
		"pact:proto": "` + path + `",
		"pact:proto-service": "Primary/GetRectangle",
		"pact:content-type": "application/protobuf",
		"pact:protobuf-config": {
			"additionalIncludes": [
				"` + dir + `"
			]	
		},
		"request": {
			"x": "matching(number, 180)",
			"y": "matching(number, 200)",
			"width": "matching(number, 10)",
			"length": "matching(number, 20)"
		},
		"response": {
			"rectangle": {
				"lo": {
					"latitude": "matching(number, 180)",
					"longitude": "matching(number, 99)"
				},
				"hi": {
					"latitude": "matching(number, 200)",
					"longitude": "matching(number, 99)"
				}
			}
		}
	}`

	err := p.AddSynchronousMessage("Primary - GetRectangle").
		Given("Primary GetRectangle exists").
		UsingPlugin(message.PluginConfig{
			Plugin:  "protobuf",
			Version: "0.3.13",
		}).
		WithContents(grpcInteraction, "application/protobuf").
		StartTransport("grpc", "127.0.0.1", nil). // For plugin tests, we can't assume if a transport is needed, so this is optional
		ExecuteTest(t, func(transport message.TransportConfig, m message.SynchronousMessage) error {
			fmt.Println("gRPC transport running on", transport)

			// Establish the gRPC connection
			conn, err := grpc.Dial(fmt.Sprintf("127.0.0.1:%d", transport.Port), grpc.WithTransportCredentials(insecure.NewCredentials()))
			if err != nil {
				t.Fatal("unable to communicate to grpc server", err)
			}
			defer conn.Close()

			// Create the gRPC client
			c := primary.NewPrimaryClient(conn)

			req := &primary.RectangleLocationRequest{
				X:      180,
				Y:      200,
				Width:  10,
				Length: 20,
			}

			// Now we can make a normal gRPC request
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			resp, err := c.GetRectangle(ctx, req)
			require.NoError(t, err)
			rectangle := resp.GetRectangle()
			require.NotNil(t, rectangle)

			return nil
		})

	assert.NoError(t, err)
}
