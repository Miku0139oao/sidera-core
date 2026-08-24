package ccm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/Miku0139oao/sidera-core/log"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/service/filemanager"
)

type UsageStats struct {
	RequestCount                    int   `json:"request_count"`
	MessagesCount                   int   `json:"messages_count"`
	InputTokens                     int64 `json:"input_tokens"`
	OutputTokens                    int64 `json:"output_tokens"`
	CacheReadInputTokens            int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens        int64 `json:"cache_creation_input_tokens"`
	CacheCreation5MinuteInputTokens int64 `json:"cache_creation_5m_input_tokens,omitempty"`
	CacheCreation1HourInputTokens   int64 `json:"cache_creation_1h_input_tokens,omitempty"`
}

type CostCombination struct {
	Model         string                `json:"model"`
	ContextWindow int                   `json:"context_window"`
	WeekStartUnix int64                 `json:"week_start_unix,omitempty"`
	Total         UsageStats            `json:"total"`
	ByUser        map[string]UsageStats `json:"by_user"`
}

type AggregatedUsage struct {
	LastUpdated  time.Time         `json:"last_updated"`
	Combinations []CostCombination `json:"combinations"`
	mutex        sync.Mutex
	ctx          context.Context
	filePath     string
	logger       log.ContextLogger

	saveMutex      sync.Mutex
	saveWriter     func([]byte) error
	workerAccess   sync.Mutex
	workerWake     chan struct{}
	workerDone     chan struct{}
	closeDone      chan struct{}
	workerStarted  bool
	closing        bool
	closed         bool
	dirty          bool
	generation     uint64
	lastSaveTime   time.Time
	saveInterval   time.Duration
	persistenceErr error
}

type UsageStatsJSON struct {
	RequestCount                    int     `json:"request_count"`
	MessagesCount                   int     `json:"messages_count"`
	InputTokens                     int64   `json:"input_tokens"`
	OutputTokens                    int64   `json:"output_tokens"`
	CacheReadInputTokens            int64   `json:"cache_read_input_tokens"`
	CacheCreationInputTokens        int64   `json:"cache_creation_input_tokens"`
	CacheCreation5MinuteInputTokens int64   `json:"cache_creation_5m_input_tokens,omitempty"`
	CacheCreation1HourInputTokens   int64   `json:"cache_creation_1h_input_tokens,omitempty"`
	CostUSD                         float64 `json:"cost_usd"`
}

type CostCombinationJSON struct {
	Model         string                    `json:"model"`
	ContextWindow int                       `json:"context_window"`
	WeekStartUnix int64                     `json:"week_start_unix,omitempty"`
	Total         UsageStatsJSON            `json:"total"`
	ByUser        map[string]UsageStatsJSON `json:"by_user"`
}

type CostsSummaryJSON struct {
	TotalUSD      float64                       `json:"total_usd"`
	ByUser        map[string]float64            `json:"by_user"`
	ByWeek        map[string]float64            `json:"by_week,omitempty"`
	ByUserAndWeek map[string]map[string]float64 `json:"by_user_and_week,omitempty"`
}

type AggregatedUsageJSON struct {
	LastUpdated  time.Time             `json:"last_updated"`
	Costs        CostsSummaryJSON      `json:"costs"`
	Combinations []CostCombinationJSON `json:"combinations"`
}

type WeeklyCycleHint struct {
	WindowMinutes int64
	ResetAt       time.Time
}

type ModelPricing struct {
	InputPrice             float64
	OutputPrice            float64
	CacheReadPrice         float64
	CacheWritePrice5Minute float64
	CacheWritePrice1Hour   float64
}

type modelFamily struct {
	pattern         *regexp.Regexp
	standardPricing ModelPricing
	premiumPricing  *ModelPricing
}

var (
	opus46StandardPricing = ModelPricing{
		InputPrice:             5.0,
		OutputPrice:            25.0,
		CacheReadPrice:         0.5,
		CacheWritePrice5Minute: 6.25,
		CacheWritePrice1Hour:   10.0,
	}

	opus46PremiumPricing = ModelPricing{
		InputPrice:             10.0,
		OutputPrice:            37.5,
		CacheReadPrice:         1.0,
		CacheWritePrice5Minute: 12.5,
		CacheWritePrice1Hour:   20.0,
	}

	opus45Pricing = ModelPricing{
		InputPrice:             5.0,
		OutputPrice:            25.0,
		CacheReadPrice:         0.5,
		CacheWritePrice5Minute: 6.25,
		CacheWritePrice1Hour:   10.0,
	}

	opus4Pricing = ModelPricing{
		InputPrice:             15.0,
		OutputPrice:            75.0,
		CacheReadPrice:         1.5,
		CacheWritePrice5Minute: 18.75,
		CacheWritePrice1Hour:   30.0,
	}

	sonnet46StandardPricing = ModelPricing{
		InputPrice:             3.0,
		OutputPrice:            15.0,
		CacheReadPrice:         0.3,
		CacheWritePrice5Minute: 3.75,
		CacheWritePrice1Hour:   6.0,
	}

	sonnet46PremiumPricing = ModelPricing{
		InputPrice:             6.0,
		OutputPrice:            22.5,
		CacheReadPrice:         0.6,
		CacheWritePrice5Minute: 7.5,
		CacheWritePrice1Hour:   12.0,
	}

	sonnet45StandardPricing = ModelPricing{
		InputPrice:             3.0,
		OutputPrice:            15.0,
		CacheReadPrice:         0.3,
		CacheWritePrice5Minute: 3.75,
		CacheWritePrice1Hour:   6.0,
	}

	sonnet45PremiumPricing = ModelPricing{
		InputPrice:             6.0,
		OutputPrice:            22.5,
		CacheReadPrice:         0.6,
		CacheWritePrice5Minute: 7.5,
		CacheWritePrice1Hour:   12.0,
	}

	sonnet4StandardPricing = ModelPricing{
		InputPrice:             3.0,
		OutputPrice:            15.0,
		CacheReadPrice:         0.3,
		CacheWritePrice5Minute: 3.75,
		CacheWritePrice1Hour:   6.0,
	}

	sonnet4PremiumPricing = ModelPricing{
		InputPrice:             6.0,
		OutputPrice:            22.5,
		CacheReadPrice:         0.6,
		CacheWritePrice5Minute: 7.5,
		CacheWritePrice1Hour:   12.0,
	}

	sonnet37Pricing = ModelPricing{
		InputPrice:             3.0,
		OutputPrice:            15.0,
		CacheReadPrice:         0.3,
		CacheWritePrice5Minute: 3.75,
		CacheWritePrice1Hour:   6.0,
	}

	sonnet35Pricing = ModelPricing{
		InputPrice:             3.0,
		OutputPrice:            15.0,
		CacheReadPrice:         0.3,
		CacheWritePrice5Minute: 3.75,
		CacheWritePrice1Hour:   6.0,
	}

	haiku45Pricing = ModelPricing{
		InputPrice:             1.0,
		OutputPrice:            5.0,
		CacheReadPrice:         0.1,
		CacheWritePrice5Minute: 1.25,
		CacheWritePrice1Hour:   2.0,
	}

	haiku4Pricing = ModelPricing{
		InputPrice:             1.0,
		OutputPrice:            5.0,
		CacheReadPrice:         0.1,
		CacheWritePrice5Minute: 1.25,
		CacheWritePrice1Hour:   2.0,
	}

	haiku35Pricing = ModelPricing{
		InputPrice:             0.8,
		OutputPrice:            4.0,
		CacheReadPrice:         0.08,
		CacheWritePrice5Minute: 1.0,
		CacheWritePrice1Hour:   1.6,
	}

	haiku3Pricing = ModelPricing{
		InputPrice:             0.25,
		OutputPrice:            1.25,
		CacheReadPrice:         0.03,
		CacheWritePrice5Minute: 0.3,
		CacheWritePrice1Hour:   0.5,
	}

	opus3Pricing = ModelPricing{
		InputPrice:             15.0,
		OutputPrice:            75.0,
		CacheReadPrice:         1.5,
		CacheWritePrice5Minute: 18.75,
		CacheWritePrice1Hour:   30.0,
	}

	modelFamilies = []modelFamily{
		{
			pattern:         regexp.MustCompile(`^claude-opus-4-6(?:-|$)`),
			standardPricing: opus46StandardPricing,
			premiumPricing:  &opus46PremiumPricing,
		},
		{
			pattern:         regexp.MustCompile(`^claude-opus-4-5(?:-|$)`),
			standardPricing: opus45Pricing,
			premiumPricing:  nil,
		},
		{
			pattern:         regexp.MustCompile(`^claude-(?:opus-4(?:-|$)|4-opus-)`),
			standardPricing: opus4Pricing,
			premiumPricing:  nil,
		},
		{
			pattern:         regexp.MustCompile(`^claude-(?:opus-3(?:-|$)|3-opus-)`),
			standardPricing: opus3Pricing,
			premiumPricing:  nil,
		},
		{
			pattern:         regexp.MustCompile(`^claude-(?:sonnet-4-6(?:-|$)|4-6-sonnet-)`),
			standardPricing: sonnet46StandardPricing,
			premiumPricing:  &sonnet46PremiumPricing,
		},
		{
			pattern:         regexp.MustCompile(`^claude-(?:sonnet-4-5(?:-|$)|4-5-sonnet-)`),
			standardPricing: sonnet45StandardPricing,
			premiumPricing:  &sonnet45PremiumPricing,
		},
		{
			pattern:         regexp.MustCompile(`^claude-(?:sonnet-4(?:-|$)|4-sonnet-)`),
			standardPricing: sonnet4StandardPricing,
			premiumPricing:  &sonnet4PremiumPricing,
		},
		{
			pattern:         regexp.MustCompile(`^claude-3-7-sonnet(?:-|$)`),
			standardPricing: sonnet37Pricing,
			premiumPricing:  nil,
		},
		{
			pattern:         regexp.MustCompile(`^claude-3-5-sonnet(?:-|$)`),
			standardPricing: sonnet35Pricing,
			premiumPricing:  nil,
		},
		{
			pattern:         regexp.MustCompile(`^claude-(?:haiku-4-5(?:-|$)|4-5-haiku-)`),
			standardPricing: haiku45Pricing,
			premiumPricing:  nil,
		},
		{
			pattern:         regexp.MustCompile(`^claude-haiku-4(?:-|$)`),
			standardPricing: haiku4Pricing,
			premiumPricing:  nil,
		},
		{
			pattern:         regexp.MustCompile(`^claude-3-5-haiku(?:-|$)`),
			standardPricing: haiku35Pricing,
			premiumPricing:  nil,
		},
		{
			pattern:         regexp.MustCompile(`^claude-3-haiku(?:-|$)`),
			standardPricing: haiku3Pricing,
			premiumPricing:  nil,
		},
	}
)

func getPricing(model string, contextWindow int) ModelPricing {
	isPremium := contextWindow >= contextWindowPremium

	for _, family := range modelFamilies {
		if family.pattern.MatchString(model) {
			if isPremium && family.premiumPricing != nil {
				return *family.premiumPricing
			}
			return family.standardPricing
		}
	}

	return sonnet4StandardPricing
}

func calculateCost(stats UsageStats, model string, contextWindow int) float64 {
	pricing := getPricing(model, contextWindow)

	cacheCreationCost := 0.0
	if stats.CacheCreation5MinuteInputTokens > 0 || stats.CacheCreation1HourInputTokens > 0 {
		cacheCreationCost = float64(stats.CacheCreation5MinuteInputTokens)*pricing.CacheWritePrice5Minute +
			float64(stats.CacheCreation1HourInputTokens)*pricing.CacheWritePrice1Hour
	} else {
		// Backward compatibility for usage files generated before TTL split tracking.
		cacheCreationCost = float64(stats.CacheCreationInputTokens) * pricing.CacheWritePrice5Minute
	}

	cost := (float64(stats.InputTokens)*pricing.InputPrice +
		float64(stats.OutputTokens)*pricing.OutputPrice +
		float64(stats.CacheReadInputTokens)*pricing.CacheReadPrice +
		cacheCreationCost) / 1_000_000

	return math.Round(cost*100) / 100
}

func roundCost(cost float64) float64 {
	return math.Round(cost*100) / 100
}

const (
	maxRetainedUsagePeriods      = 52
	maxRetainedUsageCombinations = 4096
	maxUsageFileBytes            = 64 << 20
	maxUsageModelBytes           = 256
	maxUsageUserBytes            = 256
)

func normalizeCombinations(combinations []CostCombination) {
	for index := range combinations {
		if combinations[index].ByUser == nil {
			combinations[index].ByUser = make(map[string]UsageStats)
		}
	}
}

func mergeUsageStats(target *UsageStats, source UsageStats) {
	target.RequestCount += source.RequestCount
	target.MessagesCount += source.MessagesCount
	target.InputTokens += source.InputTokens
	target.OutputTokens += source.OutputTokens
	target.CacheReadInputTokens += source.CacheReadInputTokens
	target.CacheCreationInputTokens += source.CacheCreationInputTokens
	target.CacheCreation5MinuteInputTokens += source.CacheCreation5MinuteInputTokens
	target.CacheCreation1HourInputTokens += source.CacheCreation1HourInputTokens
}

func mergeCostCombination(target *CostCombination, source CostCombination) {
	mergeUsageStats(&target.Total, source.Total)
	if target.ByUser == nil {
		target.ByUser = make(map[string]UsageStats)
	}
	for user, sourceStats := range source.ByUser {
		targetStats := target.ByUser[user]
		mergeUsageStats(&targetStats, sourceStats)
		target.ByUser[user] = targetStats
	}
}

func pruneUsagePeriods(combinations *[]CostCombination) {
	weekSet := make(map[int64]struct{})
	for _, combination := range *combinations {
		if combination.WeekStartUnix > 0 {
			weekSet[combination.WeekStartUnix] = struct{}{}
		}
	}
	if len(weekSet) <= maxRetainedUsagePeriods {
		return
	}
	weeks := make([]int64, 0, len(weekSet))
	for week := range weekSet {
		weeks = append(weeks, week)
	}
	sort.Slice(weeks, func(i, j int) bool { return weeks[i] > weeks[j] })
	retainedWeeks := make(map[int64]struct{}, maxRetainedUsagePeriods)
	for _, week := range weeks[:maxRetainedUsagePeriods] {
		retainedWeeks[week] = struct{}{}
	}

	type rollupKey struct {
		model         string
		contextWindow int
	}
	result := make([]CostCombination, 0, len(*combinations))
	rollups := make(map[rollupKey]int)
	for _, combination := range *combinations {
		if combination.WeekStartUnix > 0 {
			if _, retained := retainedWeeks[combination.WeekStartUnix]; !retained {
				continue
			}
		}
		if combination.WeekStartUnix == 0 {
			key := rollupKey{combination.Model, combination.ContextWindow}
			if index, exists := rollups[key]; exists {
				mergeCostCombination(&result[index], combination)
				continue
			}
			rollups[key] = len(result)
		}
		result = append(result, combination)
	}
	for _, combination := range *combinations {
		if combination.WeekStartUnix <= 0 {
			continue
		}
		if _, retained := retainedWeeks[combination.WeekStartUnix]; retained {
			continue
		}
		key := rollupKey{combination.Model, combination.ContextWindow}
		if index, exists := rollups[key]; exists {
			mergeCostCombination(&result[index], combination)
			continue
		}
		combination.WeekStartUnix = 0
		rollups[key] = len(result)
		result = append(result, combination)
	}
	*combinations = result
}

func addUsageToCombinations(
	combinations *[]CostCombination,
	model string,
	contextWindow int,
	weekStartUnix int64,
	messagesCount int,
	inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens, cacheCreation5MinuteTokens, cacheCreation1HourTokens int64,
	user string,
) bool {
	var matchedCombination *CostCombination
	for index := range *combinations {
		combination := &(*combinations)[index]
		if combination.Model == model && combination.ContextWindow == contextWindow && combination.WeekStartUnix == weekStartUnix {
			matchedCombination = combination
			break
		}
	}

	if matchedCombination == nil {
		if len(*combinations) >= maxRetainedUsageCombinations {
			return false
		}
		newCombination := CostCombination{
			Model:         model,
			ContextWindow: contextWindow,
			WeekStartUnix: weekStartUnix,
			Total:         UsageStats{},
			ByUser:        make(map[string]UsageStats),
		}
		*combinations = append(*combinations, newCombination)
		matchedCombination = &(*combinations)[len(*combinations)-1]
	}

	if cacheCreationTokens == 0 {
		cacheCreationTokens = cacheCreation5MinuteTokens + cacheCreation1HourTokens
	}

	matchedCombination.Total.RequestCount++
	matchedCombination.Total.MessagesCount += messagesCount
	matchedCombination.Total.InputTokens += inputTokens
	matchedCombination.Total.OutputTokens += outputTokens
	matchedCombination.Total.CacheReadInputTokens += cacheReadTokens
	matchedCombination.Total.CacheCreationInputTokens += cacheCreationTokens
	matchedCombination.Total.CacheCreation5MinuteInputTokens += cacheCreation5MinuteTokens
	matchedCombination.Total.CacheCreation1HourInputTokens += cacheCreation1HourTokens

	if user != "" {
		userStats := matchedCombination.ByUser[user]
		userStats.RequestCount++
		userStats.MessagesCount += messagesCount
		userStats.InputTokens += inputTokens
		userStats.OutputTokens += outputTokens
		userStats.CacheReadInputTokens += cacheReadTokens
		userStats.CacheCreationInputTokens += cacheCreationTokens
		userStats.CacheCreation5MinuteInputTokens += cacheCreation5MinuteTokens
		userStats.CacheCreation1HourInputTokens += cacheCreation1HourTokens
		matchedCombination.ByUser[user] = userStats
	}
	return true
}

func buildCombinationJSON(combinations []CostCombination, aggregateUserCosts map[string]float64) ([]CostCombinationJSON, float64) {
	result := make([]CostCombinationJSON, len(combinations))
	var totalCost float64

	for index, combination := range combinations {
		combinationTotalCost := calculateCost(combination.Total, combination.Model, combination.ContextWindow)
		totalCost += combinationTotalCost

		combinationJSON := CostCombinationJSON{
			Model:         combination.Model,
			ContextWindow: combination.ContextWindow,
			WeekStartUnix: combination.WeekStartUnix,
			Total: UsageStatsJSON{
				RequestCount:                    combination.Total.RequestCount,
				MessagesCount:                   combination.Total.MessagesCount,
				InputTokens:                     combination.Total.InputTokens,
				OutputTokens:                    combination.Total.OutputTokens,
				CacheReadInputTokens:            combination.Total.CacheReadInputTokens,
				CacheCreationInputTokens:        combination.Total.CacheCreationInputTokens,
				CacheCreation5MinuteInputTokens: combination.Total.CacheCreation5MinuteInputTokens,
				CacheCreation1HourInputTokens:   combination.Total.CacheCreation1HourInputTokens,
				CostUSD:                         combinationTotalCost,
			},
			ByUser: make(map[string]UsageStatsJSON),
		}

		for user, userStats := range combination.ByUser {
			userCost := calculateCost(userStats, combination.Model, combination.ContextWindow)
			if aggregateUserCosts != nil {
				aggregateUserCosts[user] += userCost
			}

			combinationJSON.ByUser[user] = UsageStatsJSON{
				RequestCount:                    userStats.RequestCount,
				MessagesCount:                   userStats.MessagesCount,
				InputTokens:                     userStats.InputTokens,
				OutputTokens:                    userStats.OutputTokens,
				CacheReadInputTokens:            userStats.CacheReadInputTokens,
				CacheCreationInputTokens:        userStats.CacheCreationInputTokens,
				CacheCreation5MinuteInputTokens: userStats.CacheCreation5MinuteInputTokens,
				CacheCreation1HourInputTokens:   userStats.CacheCreation1HourInputTokens,
				CostUSD:                         userCost,
			}
		}

		result[index] = combinationJSON
	}

	return result, roundCost(totalCost)
}

func formatUTCOffsetLabel(timestamp time.Time) string {
	_, offsetSeconds := timestamp.Zone()
	sign := "+"
	if offsetSeconds < 0 {
		sign = "-"
		offsetSeconds = -offsetSeconds
	}
	offsetHours := offsetSeconds / 3600
	offsetMinutes := (offsetSeconds % 3600) / 60
	if offsetMinutes == 0 {
		return fmt.Sprintf("UTC%s%d", sign, offsetHours)
	}
	return fmt.Sprintf("UTC%s%d:%02d", sign, offsetHours, offsetMinutes)
}

func formatWeekStartKey(cycleStartAt time.Time) string {
	localCycleStart := cycleStartAt.In(time.Local)
	return fmt.Sprintf("%s %s", localCycleStart.Format("2006-01-02 15:04:05"), formatUTCOffsetLabel(localCycleStart))
}

func buildByWeekCost(combinations []CostCombination) map[string]float64 {
	byWeek := make(map[string]float64)
	for _, combination := range combinations {
		if combination.WeekStartUnix <= 0 {
			continue
		}
		weekStartAt := time.Unix(combination.WeekStartUnix, 0).UTC()
		weekKey := formatWeekStartKey(weekStartAt)
		byWeek[weekKey] += calculateCost(combination.Total, combination.Model, combination.ContextWindow)
	}
	for weekKey, weekCost := range byWeek {
		byWeek[weekKey] = roundCost(weekCost)
	}
	return byWeek
}

func buildByUserAndWeekCost(combinations []CostCombination) map[string]map[string]float64 {
	byUserAndWeek := make(map[string]map[string]float64)
	for _, combination := range combinations {
		if combination.WeekStartUnix <= 0 {
			continue
		}
		weekStartAt := time.Unix(combination.WeekStartUnix, 0).UTC()
		weekKey := formatWeekStartKey(weekStartAt)
		for user, userStats := range combination.ByUser {
			userWeeks, exists := byUserAndWeek[user]
			if !exists {
				userWeeks = make(map[string]float64)
				byUserAndWeek[user] = userWeeks
			}
			userWeeks[weekKey] += calculateCost(userStats, combination.Model, combination.ContextWindow)
		}
	}
	for _, weekCosts := range byUserAndWeek {
		for weekKey, cost := range weekCosts {
			weekCosts[weekKey] = roundCost(cost)
		}
	}
	return byUserAndWeek
}

func deriveWeekStartUnix(cycleHint *WeeklyCycleHint) int64 {
	if cycleHint == nil || cycleHint.WindowMinutes <= 0 || cycleHint.ResetAt.IsZero() {
		return 0
	}
	windowDuration := time.Duration(cycleHint.WindowMinutes) * time.Minute
	return cycleHint.ResetAt.UTC().Add(-windowDuration).Unix()
}

func (u *AggregatedUsage) ToJSON() *AggregatedUsageJSON {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	result := &AggregatedUsageJSON{
		LastUpdated: u.LastUpdated,
		Costs: CostsSummaryJSON{
			TotalUSD: 0,
			ByUser:   make(map[string]float64),
			ByWeek:   make(map[string]float64),
		},
	}

	globalCombinationsJSON, totalCost := buildCombinationJSON(u.Combinations, result.Costs.ByUser)
	result.Combinations = globalCombinationsJSON
	result.Costs.TotalUSD = totalCost
	result.Costs.ByWeek = buildByWeekCost(u.Combinations)

	if len(result.Costs.ByWeek) == 0 {
		result.Costs.ByWeek = nil
	}

	result.Costs.ByUserAndWeek = buildByUserAndWeekCost(u.Combinations)
	if len(result.Costs.ByUserAndWeek) == 0 {
		result.Costs.ByUserAndWeek = nil
	}

	for user, cost := range result.Costs.ByUser {
		result.Costs.ByUser[user] = roundCost(cost)
	}

	return result
}

func (u *AggregatedUsage) Load() error {
	u.mutex.Lock()
	defer u.mutex.Unlock()

	u.LastUpdated = time.Time{}
	u.Combinations = nil

	file, err := filemanager.OpenFile(u.ctx, u.filePath, os.O_RDONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	data, oversized, readErr := readBoundedBody(file, maxUsageFileBytes)
	closeErr := file.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if oversized {
		return E.New("usage history file exceeds limit")
	}

	var temp struct {
		LastUpdated  time.Time         `json:"last_updated"`
		Combinations []CostCombination `json:"combinations"`
	}

	err = json.Unmarshal(data, &temp)
	if err != nil {
		return err
	}

	normalizeCombinations(temp.Combinations)
	pruneUsagePeriods(&temp.Combinations)
	if len(temp.Combinations) > maxRetainedUsageCombinations {
		return E.New("usage history combination limit exceeded")
	}
	u.LastUpdated = temp.LastUpdated
	u.Combinations = temp.Combinations
	return nil
}

func (u *AggregatedUsage) Save() error {
	u.saveMutex.Lock()
	defer u.saveMutex.Unlock()

	jsonData := u.ToJSON()
	data, err := json.MarshalIndent(jsonData, "", "  ")
	if err != nil {
		return err
	}
	if u.saveWriter != nil {
		return u.saveWriter(data)
	}
	return u.writeUsageFile(data)
}

func (u *AggregatedUsage) writeUsageFile(data []byte) error {
	var randomSuffix [8]byte
	if _, err := rand.Read(randomSuffix[:]); err != nil {
		return err
	}
	tmpFile := u.filePath + ".tmp." + hex.EncodeToString(randomSuffix[:])
	file, err := filemanager.OpenFile(u.ctx, tmpFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		_ = filemanager.Remove(u.ctx, tmpFile)
		return writeErr
	}
	if closeErr != nil {
		_ = filemanager.Remove(u.ctx, tmpFile)
		return closeErr
	}
	defer filemanager.Remove(u.ctx, tmpFile)
	return filemanager.Rename(u.ctx, tmpFile, u.filePath)
}

func (u *AggregatedUsage) AddUsage(
	model string,
	contextWindow int,
	messagesCount int,
	inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens, cacheCreation5MinuteTokens, cacheCreation1HourTokens int64,
	user string,
) error {
	return u.AddUsageWithCycleHint(model, contextWindow, messagesCount, inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens, cacheCreation5MinuteTokens, cacheCreation1HourTokens, user, time.Now(), nil)
}

func (u *AggregatedUsage) AddUsageWithCycleHint(
	model string,
	contextWindow int,
	messagesCount int,
	inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens, cacheCreation5MinuteTokens, cacheCreation1HourTokens int64,
	user string,
	observedAt time.Time,
	cycleHint *WeeklyCycleHint,
) error {
	if model == "" {
		return E.New("model cannot be empty")
	}
	if len(model) > maxUsageModelBytes {
		return E.New("model exceeds usage history limit")
	}
	if len(user) > maxUsageUserBytes {
		return E.New("user exceeds usage history limit")
	}
	if contextWindow <= 0 {
		return E.New("contextWindow must be positive")
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}

	u.workerAccess.Lock()
	if u.closing || u.closed {
		u.workerAccess.Unlock()
		return os.ErrClosed
	}
	u.mutex.Lock()
	weekStartUnix := deriveWeekStartUnix(cycleHint)
	pruneUsagePeriods(&u.Combinations)
	if !addUsageToCombinations(&u.Combinations, model, contextWindow, weekStartUnix, messagesCount, inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens, cacheCreation5MinuteTokens, cacheCreation1HourTokens, user) {
		u.mutex.Unlock()
		u.workerAccess.Unlock()
		return E.New("usage history combination limit reached")
	}
	pruneUsagePeriods(&u.Combinations)
	u.LastUpdated = observedAt
	u.mutex.Unlock()
	u.generation++
	u.dirty = true
	u.startSaveWorkerLocked()
	u.workerAccess.Unlock()
	u.notifySaveWorker()
	return nil
}

func (u *AggregatedUsage) startSaveWorker() {
	u.workerAccess.Lock()
	u.startSaveWorkerLocked()
	u.workerAccess.Unlock()
}

func (u *AggregatedUsage) startSaveWorkerLocked() {
	if u.workerStarted || u.closing || u.closed {
		return
	}
	if u.workerWake == nil {
		u.workerWake = make(chan struct{}, 1)
	}
	if u.workerDone == nil {
		u.workerDone = make(chan struct{})
	}
	if u.closeDone == nil {
		u.closeDone = make(chan struct{})
	}
	if u.saveInterval <= 0 {
		u.saveInterval = time.Minute
	}
	u.workerStarted = true
	go u.runSaveWorker()
}

func (u *AggregatedUsage) notifySaveWorker() {
	u.workerAccess.Lock()
	wake := u.workerWake
	u.workerAccess.Unlock()
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (u *AggregatedUsage) runSaveWorker() {
	defer close(u.workerDone)
	for {
		u.workerAccess.Lock()
		if u.closing {
			u.workerAccess.Unlock()
			return
		}
		if !u.dirty {
			wake := u.workerWake
			u.workerAccess.Unlock()
			<-wake
			continue
		}
		delay := u.saveInterval - time.Since(u.lastSaveTime)
		if u.lastSaveTime.IsZero() || delay <= 0 {
			targetGeneration := u.generation
			u.dirty = false
			u.workerAccess.Unlock()
			err := u.Save()
			u.workerAccess.Lock()
			u.lastSaveTime = time.Now()
			if err != nil || u.generation != targetGeneration {
				u.dirty = true
			}
			if err != nil {
				u.persistenceErr = err
			}
			u.workerAccess.Unlock()
			if err != nil && u.logger != nil {
				u.logger.Error("save usage statistics: ", err)
			}
			continue
		}
		wake := u.workerWake
		u.workerAccess.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (u *AggregatedUsage) closeAndSave() error {
	u.workerAccess.Lock()
	if u.closing || u.closed {
		done := u.closeDone
		u.workerAccess.Unlock()
		if done != nil {
			<-done
		}
		u.workerAccess.Lock()
		err := u.persistenceErr
		u.workerAccess.Unlock()
		return err
	}
	if u.closeDone == nil {
		u.closeDone = make(chan struct{})
	}
	u.closing = true
	workerStarted := u.workerStarted
	workerDone := u.workerDone
	wake := u.workerWake
	u.workerAccess.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	if workerStarted {
		<-workerDone
	}

	finalErr := u.Save()
	u.workerAccess.Lock()
	if finalErr == nil {
		u.persistenceErr = nil
	} else {
		u.persistenceErr = errors.Join(u.persistenceErr, finalErr)
	}
	u.closed = true
	u.closing = false
	close(u.closeDone)
	err := u.persistenceErr
	u.workerAccess.Unlock()
	return err
}
