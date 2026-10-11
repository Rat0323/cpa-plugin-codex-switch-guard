//go:build cgo

package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

static int CodexSwitchSafeHostCall(cliproxy_host_api* host, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (host == NULL || host->call == NULL) {
		return 1;
	}
	return host->call(host->host_ctx, method, request, request_len, response);
}

static void CodexSwitchSafeHostFree(cliproxy_host_api* host, void* ptr, size_t len) {
	if (host != NULL && host->free_buffer != NULL && ptr != NULL) {
		host->free_buffer(ptr, len);
	}
}

extern int CodexSwitchSafePluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void CodexSwitchSafePluginFree(void*, size_t);
extern void CodexSwitchSafePluginShutdown(void);
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const maxCGoBytesLen = C.size_t(1<<31 - 1)

var switchSafeABIState = struct {
	sync.Mutex
	plugin       *switchSafePlugin
	shuttingDown bool
	inFlight     sync.WaitGroup
}{}

var switchSafeHostState = struct {
	sync.RWMutex
	api *C.cliproxy_host_api
}{}

type abiEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *abiError       `json:"error,omitempty"`
}

type abiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type abiLifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	configureHostAPI(host)
	switchSafeABIState.Lock()
	switchSafeABIState.shuttingDown = false
	switchSafeABIState.Unlock()

	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.CodexSwitchSafePluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.CodexSwitchSafePluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.CodexSwitchSafePluginShutdown)
	return 0
}

//export CodexSwitchSafePluginCall
func CodexSwitchSafePluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeABIResponse(response, abiErrorEnvelope("invalid_method", "method is required"))
		return 0
	}
	if requestLen > maxCGoBytesLen {
		writeABIResponse(response, abiErrorEnvelope("request_too_large", "request payload is too large"))
		return 0
	}

	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleABIMethod(context.Background(), C.GoString(method), requestBytes)
	if errHandle != nil {
		writeABIResponse(response, abiErrorEnvelope("plugin_error", errHandle.Error()))
		return 0
	}
	writeABIResponse(response, raw)
	return 0
}

//export CodexSwitchSafePluginFree
func CodexSwitchSafePluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export CodexSwitchSafePluginShutdown
func CodexSwitchSafePluginShutdown() {
	switchSafeABIState.Lock()
	switchSafeABIState.shuttingDown = true
	switchSafeABIState.plugin = nil
	switchSafeABIState.Unlock()
	switchSafeABIState.inFlight.Wait()
	clearHostAPI()
}

func configureHostAPI(host *C.cliproxy_host_api) {
	switchSafeHostState.Lock()
	defer switchSafeHostState.Unlock()
	if switchSafeHostState.api != nil {
		C.free(unsafe.Pointer(switchSafeHostState.api))
		switchSafeHostState.api = nil
	}
	if host == nil {
		return
	}
	copyAPI := (*C.cliproxy_host_api)(C.malloc(C.size_t(unsafe.Sizeof(*host))))
	if copyAPI == nil {
		return
	}
	*copyAPI = *host
	switchSafeHostState.api = copyAPI
}

func clearHostAPI() {
	switchSafeHostState.Lock()
	defer switchSafeHostState.Unlock()
	if switchSafeHostState.api != nil {
		C.free(unsafe.Pointer(switchSafeHostState.api))
		switchSafeHostState.api = nil
	}
}

func hostDiagnosticSink(level, message string, fields map[string]any) {
	payload, errMarshal := json.Marshal(map[string]any{
		"level":   level,
		"message": message,
		"fields":  fields,
	})
	if errMarshal != nil {
		return
	}

	switchSafeHostState.RLock()
	defer switchSafeHostState.RUnlock()
	if switchSafeHostState.api == nil {
		return
	}
	method := C.CString(pluginabi.MethodHostLog)
	defer C.free(unsafe.Pointer(method))
	request := C.CBytes(payload)
	defer C.free(request)
	var response C.cliproxy_buffer
	_ = C.CodexSwitchSafeHostCall(
		switchSafeHostState.api,
		method,
		(*C.uint8_t)(request),
		C.size_t(len(payload)),
		&response,
	)
	C.CodexSwitchSafeHostFree(switchSafeHostState.api, response.ptr, response.len)
}

func handleABIMethod(ctx context.Context, method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return handlePluginLifecycle(request)
	}

	p, done, errBegin := beginPluginCall()
	if errBegin != nil {
		return nil, errBegin
	}
	defer done()

	switch method {
	case pluginabi.MethodRequestInterceptBefore:
		var req pluginapi.RequestInterceptRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errIntercept := p.InterceptRequestBeforeAuth(ctx, req)
		return abiOKEnvelopeWithError(resp, errIntercept)
	case pluginabi.MethodRequestInterceptAfter:
		var req pluginapi.RequestInterceptRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errIntercept := p.InterceptRequestAfterAuth(ctx, req)
		return abiOKEnvelopeWithError(resp, errIntercept)
	case pluginabi.MethodRequestComplete:
		var completion pluginapi.RequestCompletion
		if errDecode := json.Unmarshal(request, &completion); errDecode != nil {
			return nil, errDecode
		}
		if errComplete := p.HandleRequestComplete(ctx, completion); errComplete != nil {
			return nil, errComplete
		}
		return abiOKEnvelope(struct{}{})
	case pluginabi.MethodManagementRegister:
		var req pluginapi.ManagementRegistrationRequest
		if len(request) > 0 {
			if errDecode := json.Unmarshal(request, &req); errDecode != nil {
				return nil, errDecode
			}
		}
		resp, errRegister := p.RegisterManagement(ctx, req)
		if errRegister != nil {
			return nil, errRegister
		}
		for index := range resp.Routes {
			resp.Routes[index].Handler = nil
		}
		for index := range resp.Resources {
			resp.Resources[index].Handler = nil
		}
		return abiOKEnvelope(resp)
	case pluginabi.MethodManagementHandle:
		var req pluginapi.ManagementRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errHandle := p.HandleManagement(ctx, req)
		return abiOKEnvelopeWithError(resp, errHandle)
	default:
		return abiErrorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func handlePluginLifecycle(request []byte) ([]byte, error) {
	var req abiLifecycleRequest
	if len(request) > 0 {
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
	}
	if req.SchemaVersion < 2 {
		return nil, fmt.Errorf("%s requires host schema version 2 or newer", pluginID)
	}
	schemaVersion := req.SchemaVersion
	if schemaVersion > pluginSchemaVersion {
		schemaVersion = pluginSchemaVersion
	}
	p, errBuild := buildPlugin(req.ConfigYAML)
	if errBuild != nil {
		return nil, errBuild
	}
	switchSafeABIState.Lock()
	if switchSafeABIState.shuttingDown {
		switchSafeABIState.Unlock()
		return nil, fmt.Errorf("%s is shutting down", pluginID)
	}
	switchSafeABIState.plugin = p
	switchSafeABIState.Unlock()
	return abiOKEnvelope(pluginRegistration(schemaVersion))
}

func beginPluginCall() (*switchSafePlugin, func(), error) {
	switchSafeABIState.Lock()
	defer switchSafeABIState.Unlock()
	if switchSafeABIState.shuttingDown {
		return nil, nil, fmt.Errorf("%s is shutting down", pluginID)
	}
	if switchSafeABIState.plugin == nil {
		return nil, nil, fmt.Errorf("%s is not registered", pluginID)
	}
	switchSafeABIState.inFlight.Add(1)
	return switchSafeABIState.plugin, switchSafeABIState.inFlight.Done, nil
}

func abiOKEnvelopeWithError(value any, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	return abiOKEnvelope(value)
}

func abiOKEnvelope(value any) ([]byte, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(abiEnvelope{OK: true, Result: raw})
}

func abiErrorEnvelope(code, message string) []byte {
	raw, errMarshal := json.Marshal(abiEnvelope{OK: false, Error: &abiError{Code: code, Message: message}})
	if errMarshal != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"encode error"}}`)
	}
	return raw
}

func writeABIResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
