package dashboardstore

import (
	"context"
	stdjson "encoding/json"
	"errors"
	"os"
	"sort"
	"strconv"

	C "github.com/Miku0139oao/sidera-core/constant"
	"github.com/Miku0139oao/sidera-core/option"
	E "github.com/sagernet/sing/common/exceptions"
	SJSON "github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service/filemanager"
)

const (
	StoreVersion    = 6
	DefaultDataPath = "sidera-dashboard.json"
)

type storedStore struct {
	Version               int                      `json:"version"`
	Servers               map[string]*storedServer `json:"servers"`
	Subscriptions         map[string]string        `json:"subscriptions,omitempty"`
	ExternalSubscriptions map[string]string        `json:"external_subscriptions,omitempty"`
}

type storedServer struct {
	Kind     string             `json:"kind"`
	Type     string             `json:"type"`
	Config   stdjson.RawMessage `json:"config"`
	Revision int64              `json:"revision"`
	Deleted  bool               `json:"deleted"`
}

func ResolveDataPath(ctx context.Context, dataPath string) string {
	if dataPath == "" {
		dataPath = DefaultDataPath
	}
	return filemanager.BasePath(ctx, os.ExpandEnv(dataPath))
}

// MergeProfiles appends the exact dashboard profile snapshot represented by
// the configured store. A populated revision map marks an already merged copy.
func MergeProfiles(ctx context.Context, options *option.Options) error {
	var dashboardOptions *option.APIDashboardOptions
	for _, serviceOptions := range options.Services {
		if serviceOptions.Type != C.TypeAPI {
			continue
		}
		apiOptions, loaded := serviceOptions.Options.(*option.APIServiceOptions)
		if !loaded || apiOptions.Dashboard == nil || !apiOptions.Dashboard.Enabled {
			continue
		}
		if dashboardOptions != nil {
			return E.New("only one dashboard-enabled API service is allowed")
		}
		dashboardOptions = apiOptions.Dashboard
	}
	if dashboardOptions == nil || dashboardOptions.AppliedServerRevisions != nil {
		return nil
	}
	revisions := make(map[string]int64)
	mergedInbounds := append([]option.Inbound(nil), options.Inbounds...)
	mergedEndpoints := append([]option.Endpoint(nil), options.Endpoints...)

	usedTags := make(map[string]bool, len(options.Inbounds)+len(options.Endpoints))
	for index, inbound := range options.Inbounds {
		tag := inbound.Tag
		if tag == "" {
			tag = strconv.Itoa(index)
		}
		if usedTags[tag] {
			return E.New("runtime tag collides with base configuration: ", tag)
		}
		usedTags[tag] = true
	}
	for index, endpoint := range options.Endpoints {
		tag := endpoint.Tag
		if tag == "" {
			tag = strconv.Itoa(index)
		}
		if usedTags[tag] {
			return E.New("runtime tag collides with base configuration: ", tag)
		}
		usedTags[tag] = true
	}

	stored, err := loadStore(ctx, ResolveDataPath(ctx, dashboardOptions.DataPath))
	if err != nil {
		return err
	}
	serverTags := make([]string, 0, len(stored.Servers))
	for tag := range stored.Servers {
		serverTags = append(serverTags, tag)
	}
	sort.Strings(serverTags)
	for _, tag := range serverTags {
		profile := stored.Servers[tag]
		if profile == nil {
			return E.New("invalid null dashboard server: ", tag)
		}
		if usedTags[tag] {
			return E.New("dashboard server tag collides with base configuration: ", tag)
		}
		revisions[tag] = profile.Revision
		if profile.Deleted {
			continue
		}
		switch profile.Kind {
		case "inbound":
			var inbound option.Inbound
			if err = SJSON.UnmarshalContext(ctx, profile.Config, &inbound); err != nil {
				return E.Cause(err, "decode dashboard inbound ", tag)
			}
			if inbound.Tag != tag || inbound.Type != profile.Type {
				return E.New("dashboard inbound identity mismatch: ", tag)
			}
			mergedInbounds = append(mergedInbounds, inbound)
		case "endpoint":
			var endpoint option.Endpoint
			if err = SJSON.UnmarshalContext(ctx, profile.Config, &endpoint); err != nil {
				return E.Cause(err, "decode dashboard endpoint ", tag)
			}
			if endpoint.Tag != tag || endpoint.Type != profile.Type {
				return E.New("dashboard endpoint identity mismatch: ", tag)
			}
			mergedEndpoints = append(mergedEndpoints, endpoint)
		default:
			return E.New("unsupported dashboard server kind: ", profile.Kind)
		}
		usedTags[tag] = true
	}
	options.Inbounds = mergedInbounds
	options.Endpoints = mergedEndpoints
	dashboardOptions.AppliedServerRevisions = revisions
	return nil
}

func loadStore(ctx context.Context, dataPath string) (storedStore, error) {
	content, err := filemanager.ReadFile(ctx, dataPath)
	if errors.Is(err, os.ErrNotExist) {
		content, err = filemanager.ReadFile(ctx, dataPath+".bak")
		if errors.Is(err, os.ErrNotExist) {
			return storedStore{}, nil
		}
	}
	if err != nil {
		return storedStore{}, E.Cause(err, "read dashboard data")
	}
	var stored storedStore
	if err = stdjson.Unmarshal(content, &stored); err != nil {
		return storedStore{}, E.Cause(err, "decode dashboard data")
	}
	if stored.Version != 1 && stored.Version != 2 && stored.Version != 3 && stored.Version != 4 && stored.Version != 5 && stored.Version != StoreVersion {
		return storedStore{}, E.New("unsupported dashboard data version: ", stored.Version)
	}
	return stored, nil
}
