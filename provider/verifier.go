package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pact-foundation/pact-go/v2/command"
	"github.com/pact-foundation/pact-go/v2/internal/native"
	"github.com/pact-foundation/pact-go/v2/message"
	"github.com/pact-foundation/pact-go/v2/models"
	"github.com/pact-foundation/pact-go/v2/proxy"
	"github.com/pact-foundation/pact-go/v2/utils"
)

// Verifier is used to verify the provider side of an HTTP API contract
type Verifier struct {
	// ClientTimeout specifies how long to wait for Pact CLI to start
	// Can be increased to reduce likelihood of intermittent failure
	// Defaults to 10s
	ClientTimeout time.Duration

	// Hostname to run any
	Hostname string

	handle *native.Verifier
}

func NewVerifier() *Verifier {
	native.Init()

	return &Verifier{
		handle: native.NewVerifier("pact-go", command.Version),
	}

}

func (v *Verifier) validateConfig() error {
	if v.ClientTimeout == 0 {
		v.ClientTimeout = 10 * time.Second
	}
	if v.Hostname == "" {
		v.Hostname = "localhost"
	}

	return nil
}

// If no HTTP server is given, we must start one up in order
// to provide a target for state changes etc.
func (v *Verifier) startDefaultHTTPServer(port int) {
	mux := http.NewServeMux()

	http.ListenAndServe(fmt.Sprintf("%s:%d", v.Hostname, port), mux)
}

// VerifyProviderRaw reads the provided pact files and runs verification against
// a running Provider API, providing raw response from the Verification process.
//
// Order of events: BeforeEach, stateHandlers, requestFilter(pre <execute provider> post), AfterEach
func (v *Verifier) verifyProviderRaw(request VerifyRequest, writer outputWriter) error {

	// proxy target
	var u *url.URL

	err := v.validateConfig()
	if err != nil {
		return err
	}

	// Check if a provider has been given. If none, start a dummy service to attach the proxy to
	if request.ProviderBaseURL == "" {
		log.Println("[DEBUG] setting up a dummy server for verification, as none was provided")
		port, err := utils.GetFreePort()
		if err != nil {
			log.Panic("unable to allocate a port for verification:", err)
		}
		go v.startDefaultHTTPServer(port)

		request.ProviderBaseURL = fmt.Sprintf("http://localhost:%d", port)
	}

	err = request.validate(v.handle)
	if err != nil {
		return err
	}

	u, err = url.Parse(request.ProviderBaseURL)
	if err != nil {
		log.Panic("unable to parse the provider URL", err)
	}

	m := []proxy.Middleware{}

	if request.BeforeEach != nil {
		m = append(m, beforeEachMiddleware(request.BeforeEach))
	}

	if request.AfterEach != nil {
		m = append(m, afterEachMiddleware(request.AfterEach))
	}

	if len(request.StateHandlers) > 0 {
		m = append(m, stateHandlerMiddleware(request.StateHandlers))
	}

	if len(request.MessageHandlers) > 0 {
		m = append(m, message.CreateMessageHandler(request.MessageHandlers))
	}

	if request.RequestFilter != nil {
		m = append(m, request.RequestFilter)
	}

	// Configure HTTP Verification Proxy
	opts := proxy.Options{
		TargetAddress:             fmt.Sprintf("%s:%s", u.Hostname(), u.Port()),
		TargetScheme:              u.Scheme,
		TargetPath:                u.Path,
		Middleware:                m,
		InternalRequestPathPrefix: providerStatesSetupPath,
		CustomTLSConfig:           request.CustomTLSConfig,
	}

	// Starts the message wrapper API with hooks back to the state handlers
	// This maps the 'description' field of a message pact, to a function handler
	// that will implement the message producer. This function must return an object and optionally
	// and error. The object will be marshalled to JSON for comparison.
	port, err := proxy.HTTPReverseProxy(opts)

	// Modify any existing HTTP target to the proxy instead
	// if t != nil {
	// 	t.Port = uint16(getPort(u.Port()))
	// }

	// Add any message targets
	// TODO: properly parameterise these magic strings
	if len(request.MessageHandlers) > 0 {
		request.Transports = append(request.Transports, Transport{
			Path:     "/__messages",
			Protocol: "message",
			Port:     uint16(port),
		})
	}

	// Backwards compatibility, setup old provider states URL if given
	// Otherwise point to proxy
	if request.ProviderStatesSetupURL == "" && len(request.StateHandlers) > 0 {
		request.ProviderStatesSetupURL = fmt.Sprintf("http://localhost:%d%s", port, providerStatesSetupPath)
	}

	portErr := WaitForPort(port, "tcp", "localhost", v.ClientTimeout,
		fmt.Sprintf(`Timed out waiting for http verification proxy on port %d - check for errors`, port))

	if portErr != nil {
		log.Fatal("Error:", err)
		return portErr
	}

	log.Println("[DEBUG] pact provider verification")

	return request.Verify(v.handle, writer)
}

// VerifyProvider accepts an instance of `*testing.T`
// running the provider verification with granular test reporting and
// automatic failure reporting for nice, simple tests.
func (v *Verifier) VerifyProvider(t *testing.T, request VerifyRequest) error {
	err := v.verifyProviderRaw(request, t)

	// TODO: granular test reporting
	// runTestCases(t, res)

	t.Run("Provider pact verification", func(t *testing.T) {
		if err != nil {
			t.Error(err)
		}
	})

	return err
}

// beforeEachMiddleware is invoked before any other, only on the __setup
// request (to avoid duplication)
func beforeEachMiddleware(BeforeEach Hook) proxy.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == providerStatesSetupPath {

				log.Println("[DEBUG] executing before hook")
				err := BeforeEach()

				if err != nil {
					log.Println("[ERROR] error executing before hook:", err)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// afterEachMiddleware is invoked after any other, and is the last
// function to be called prior to returning to the test suite. It is
// therefore not invoked on __setup
func afterEachMiddleware(AfterEach Hook) proxy.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)

			if r.URL.Path != providerStatesSetupPath {
				log.Println("[DEBUG] executing after hook")
				err := AfterEach()

				if err != nil {
					log.Println("[ERROR] error executing after hook:", err)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}
		})
	}
}

// {"action":"teardown","id":"foo","state":"User foo exists"}
type stateHandlerAction struct {
	Action string `json:"action"`
	State  string `json:"state"`
	Params map[string]interface{}
}

// stateHandlerMiddleware responds to the various states that are
// given during provider verification
//
// statehandler accepts a state object from the verifier and executes
// any state handlers associated with the provider.
// It will not execute further middleware if it is the designted "state" request
func stateHandlerMiddleware(stateHandlers models.StateHandlers) proxy.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == providerStatesSetupPath {
				log.Println("[INFO] executing state handler middleware")
				var state stateHandlerAction
				buf := new(strings.Builder)
				io.Copy(buf, r.Body)
				log.Println("[TRACE] state handler received raw input", buf.String())

				err := json.Unmarshal([]byte(buf.String()), &state)
				log.Println("[TRACE] state handler parsed input (without params)", state)

				if err != nil {
					log.Println("[ERROR] unable to decode incoming state change payload", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}

				// Extract the params from the payload. They are in the root, so we need to do some more work to achieve this
				var params models.ProviderStateResponse
				err = json.Unmarshal([]byte(buf.String()), &params)
				if err != nil {
					log.Println("[ERROR] unable to decode incoming state change payload", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}

				// TODO: update rust code - params should be in a sub-property, to avoid top-level key conflicts
				// i.e. it's possible action/state are actually something a users wants to supply
				delete(params, "action")
				delete(params, "state")
				state.Params = params
				log.Println("[TRACE] state handler completed parsing input (with params)", state)

				// Find the provider state handler
				sf, stateFound := stateHandlers[state.State]

				if !stateFound {
					log.Printf("[WARN] no state handler found for state: %v", state.State)
				} else {
					// Execute state handler
					res, err := sf(state.Action == "setup", models.ProviderState{Name: state.State, Parameters: state.Params})

					if err != nil {
						log.Printf("[ERROR] state handler for '%v' errored: %v", state.State, err)
						w.WriteHeader(http.StatusInternalServerError)
						return
					}

					// Return provider state values for generator
					if res != nil {
						log.Println("[TRACE] returning values from provider state (raw)", res)
						resBody, err := json.Marshal(res)
						log.Println("[TRACE] returning values from provider state (JSON)", string(resBody))

						if err != nil {
							log.Printf("[ERROR] state handler for '%v' errored: %v", state.State, err)
							w.WriteHeader(http.StatusInternalServerError)

							return
						}

						w.Header().Add("content-type", "application/json")
						w.Write(resBody)
					}
				}

				w.WriteHeader(http.StatusOK)
				return
			}

			log.Println("[TRACE] skipping state handler for request", r.RequestURI)

			// Pass through to application
			next.ServeHTTP(w, r)
		})
	}
}

// Use this to wait for a port to be running prior
// to running tests.
func WaitForPort(port int, network string, address string, timeoutDuration time.Duration, message string) error {
	log.Println("[DEBUG] waiting for port", port, "to become available")
	timeout := time.After(timeoutDuration)

	for {
		select {
		case <-timeout:
			log.Printf("[ERROR] expected server to start < %s. %s", timeoutDuration, message)
			return fmt.Errorf("expected server to start < %s. %s", timeoutDuration, message)
		case <-time.After(50 * time.Millisecond):
			_, err := net.Dial(network, fmt.Sprintf("%s:%d", address, port))
			if err == nil {
				return nil
			}
		}
	}
}

const providerStatesSetupPath = "/__setup"
