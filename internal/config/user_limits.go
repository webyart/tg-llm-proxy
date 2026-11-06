package config

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	DefaultUserDailyModelLimit       int64 = 0
	DefaultUserDailyNetworkToolLimit int64 = 0
	UserDailyLimitMax                int64 = 1000000
)

type UserDailyLimits struct {
	Model        int64
	NetworkTools int64
}

func (c Config) DailyLimitsFor(userID int64) UserDailyLimits {
	limits := UserDailyLimits{
		Model:        c.UserDailyModelLimit,
		NetworkTools: c.UserDailyNetworkToolLimit,
	}
	if override, ok := c.UserLimits[userID]; ok {
		return override
	}
	return limits
}

func loadUserDailyLimits(raw map[string]string, cfg *Config) []string {
	problems := []string{}
	modelLimit, problem := Num(raw, "USER_DAILY_MODEL_LIMIT", DefaultUserDailyModelLimit, 0, UserDailyLimitMax)
	cfg.UserDailyModelLimit = modelLimit
	problems = Push(problems, problem)
	networkLimit, problem := Num(raw, "USER_DAILY_NETWORK_TOOL_LIMIT", DefaultUserDailyNetworkToolLimit, 0, UserDailyLimitMax)
	cfg.UserDailyNetworkToolLimit = networkLimit
	problems = Push(problems, problem)

	cfg.UserLimits = map[int64]UserDailyLimits{}
	text := strings.TrimSpace(raw["USER_LIMITS_JSON"])
	if text == "" {
		text = "{}"
	}
	overrides, ok := parseUserDailyLimits(text, UserDailyLimits{Model: modelLimit, NetworkTools: networkLimit})
	if !ok {
		return Push(problems, fmt.Sprintf(
			"ENV USER_LIMITS_JSON is invalid: expected at most %d positive user ids with only model/network_tools integers between 0 and %d; {} is used instead",
			UserIDsMax, UserDailyLimitMax))
	}
	cfg.UserLimits = overrides
	return problems
}

func parseUserDailyLimits(text string, defaults UserDailyLimits) (map[int64]UserDailyLimits, bool) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if !jsonObjectStart(decoder) {
		return nil, false
	}
	limits := map[int64]UserDailyLimits{}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return nil, false
		}
		seen[key] = true
		userID, err := strconv.ParseInt(key, 10, 64)
		if err != nil || userID <= 0 || strconv.FormatInt(userID, 10) != key {
			return nil, false
		}
		override, ok := parseUserDailyOverride(decoder, defaults)
		if !ok {
			return nil, false
		}
		if len(limits) >= UserIDsMax {
			return nil, false
		}
		limits[userID] = override
	}
	if !jsonObjectEnd(decoder) || !jsonEOF(decoder) {
		return nil, false
	}
	return limits, true
}

func parseUserDailyOverride(decoder *json.Decoder, defaults UserDailyLimits) (UserDailyLimits, bool) {
	if !jsonObjectStart(decoder) {
		return UserDailyLimits{}, false
	}
	limits := defaults
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		field, ok := token.(string)
		if err != nil || !ok || seen[field] || field != "model" && field != "network_tools" {
			return UserDailyLimits{}, false
		}
		seen[field] = true
		valueToken, err := decoder.Token()
		number, ok := valueToken.(json.Number)
		if err != nil || !ok {
			return UserDailyLimits{}, false
		}
		value, err := strconv.ParseInt(number.String(), 10, 64)
		if err != nil || value < 0 || value > UserDailyLimitMax {
			return UserDailyLimits{}, false
		}
		if field == "model" {
			limits.Model = value
		} else {
			limits.NetworkTools = value
		}
	}
	if !jsonObjectEnd(decoder) {
		return UserDailyLimits{}, false
	}
	return limits, true
}

func jsonObjectStart(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	delimiter, ok := token.(json.Delim)
	return err == nil && ok && delimiter == '{'
}

func jsonObjectEnd(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	delimiter, ok := token.(json.Delim)
	return err == nil && ok && delimiter == '}'
}

func jsonEOF(decoder *json.Decoder) bool {
	_, err := decoder.Token()
	return err == io.EOF
}
