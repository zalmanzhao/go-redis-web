package main

import (
	"bytes"
	"encoding/json"
	"github.com/go-redis/redis"
	"log"
	"regexp"
	"strconv"
	"time"
	"unicode"
)

func newRedisClient(server RedisServer) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:     server.Addr,
		Password: server.Password,  // no password set
		DB:       server.DefaultDb, // use default DB
	})
}

func redisInfo(server RedisServer) string {
	client := newRedisClient(server)
	defer client.Close()

	info, _ := client.Info().Result()
	return info
}

func configGetDatabases(server RedisServer) int {
	client := newRedisClient(server)
	defer client.Close()

	config, err := client.ConfigGet("databases").Result()
	if err != nil {
		log.Println("config get databases error: ", err.Error())
		return 0
	}

	databaseNum, _ := strconv.Atoi(config[1].(string))
	return databaseNum
}

func exportRedisKeys(server RedisServer, keys, exportType string) interface{} {
	client := newRedisClient(server)
	defer client.Close()

	var exportKeys []string
	json.Unmarshal([]byte(keys), &exportKeys)

	if exportType == "Redis" {
		return exportKeysInRedisFormat(client, exportKeys)
	} else if exportType == "JSON" {
		return exportKeysInJSONFormat(client, exportKeys)
	} else {
		return ""
	}
}

func exportKeysInJSONFormat(client *redis.Client, exportKeys []string) map[string]interface{} {
	var result = make(map[string]interface{})
	for _, key := range exportKeys {
		keyType, _ := client.Type(key).Result()
		switch keyType {
		case "string":
			val, _ := client.Get(key).Result()
			result[key] = val
		case "hash":
			vals, _ := client.HGetAll(key).Result()
			result[key] = vals
		case "list":
			len, _ := client.LLen(key).Result()
			var i int64
			var items = make([]interface{}, len)
			for ; i < len; i++ {
				val, _ := client.LIndex(key, i).Result()
				items[i] = val
			}
			result[key] = items
		case "set":
			members, _ := client.SMembers(key).Result()
			result[key] = members
		case "zset":
			members, _ := client.ZRange(key, 0, -1).Result()
			result[key] = members
		}
	}

	return result
}

func exportKeysInRedisFormat(client *redis.Client, exportKeys []string) []string {
	result := make([]string, 0)
	for _, key := range exportKeys {
		keyType, _ := client.Type(key).Result()
		switch keyType {
		case "string":
			val, _ := client.Get(key).Result()
			result = append(result, `SET `+strconv.Quote(key)+` `+strconv.Quote(val))
		case "hash":
			vals, _ := client.HGetAll(key).Result()
			for k, v := range vals {
				result = append(result, `HSET `+strconv.Quote(key)+` `+strconv.Quote(k)+` `+strconv.Quote(v))
			}
		case "list":
			len, _ := client.LLen(key).Result()
			var i int64
			for ; i < len; i++ {
				val, _ := client.LIndex(key, i).Result()
				result = append(result, `RPUSH `+strconv.Quote(key)+` `+strconv.Quote(val))
			}
		case "set":
			members, _ := client.SMembers(key).Result()
			for _, member := range members {
				result = append(result, `SADD `+strconv.Quote(key)+` `+strconv.Quote(member)+`\r\n`)
			}
		case "zset":
			members, _ := client.ZRange(key, 0, -1).Result()
			for _, member := range members {
				score, _ := client.ZScore(key, member).Result()
				result = append(result, `ZADD `+strconv.Quote(key)+` `+strconv.FormatFloat(score, 'f', -1, 64)+` `+strconv.Quote(member))
			}
		}
	}

	return result
}

func newKey(server RedisServer, keyType, key, ttl, val string) string {
	client := newRedisClient(server)
	defer client.Close()

	var err error

	var duration time.Duration = -1
	if ttl != "-1s" && ttl != "" {
		duration, err = time.ParseDuration(ttl)
		if err != nil {
			return err.Error()
		}
	}

	client.Del(key)

	switch keyType {
	case "string":
		var str string
		err = json.Unmarshal([]byte(val), &str)
		if err == nil {
			val, err = strconv.Unquote(val)
			if err != nil {
				return err.Error()
			}
			_, err = client.Set(key, str, duration).Result()
		}
	case "hash":
		var hash map[string]interface{}
		err = json.Unmarshal([]byte(val), &hash)
		if err == nil {
			_, err = client.HMSet(key, hash).Result()
		}
		if err == nil && duration > 0 {
			client.Expire(key, duration)
		}
	case "set":
		var set []interface{}
		err = json.Unmarshal([]byte(val), &set)
		if err == nil {
			_, err = client.SAdd(key, set...).Result()
		}
		if err == nil && duration > 0 {
			client.Expire(key, duration)
		}
	case "list":
		var set []interface{}
		err = json.Unmarshal([]byte(val), &set)
		if err == nil {
			_, err = client.RPush(key, set...).Result()
		}
		if err == nil && duration > 0 {
			client.Expire(key, duration)
		}
	case "zset":
		var members []redis.Z
		err = json.Unmarshal([]byte(val), &members)
		if err == nil {
			_, err = client.ZAdd(key, members...).Result()
		}
		if err == nil && duration > 0 {
			client.Expire(key, duration)
		}
	}

	if err != nil {
		return err.Error()
	}

	return "OK"

}

func deleteMultiKeys(server RedisServer, keys ...string) string {
	client := newRedisClient(server)
	defer client.Close()

	_, err := client.Del(keys...).Result()
	if err != nil {
		return err.Error()
	} else {
		return "OK"
	}
}

type ContentResult struct {
	Exists   bool
	Content  interface{}
	Ttl      string
	Encoding string
	Size     int64
	Error    string
	Format   string // JSON, NORMAL, UNKNOWN
	Type     string
}

func displayContent(server RedisServer, key string, maxContentCheck bool, raw bool) *ContentResult {
	client := newRedisClient(server)
	defer client.Close()

	exists, _ := client.Exists(key).Result()
	if exists == 0 {
		return &ContentResult{
			Exists:   false,
			Content:  "",
			Ttl:      "",
			Encoding: "",
			Size:     0,
			Error:    "",
			Format:   "",
			Type:     "",
		}
	}

	var errorMessage string
	ttl, _ := client.TTL(key).Result()
	encoding, _ := client.ObjectEncoding(key).Result()
	var content interface{}
	var format string
	var err error
	var size int64

	valType, _ := client.Type(key).Result()

	switch valType {
	case "string":
		size, _ = client.StrLen(key).Result()
		if maxContentCheck && size > appConfig.MaxContentSize {
			content = "too large to display"
			format = "Unknown!"
		} else {
			content, err = client.Get(key).Result()
			if !raw && err == nil {
				content, format = parseStringFormat(content.(string))
			}
		}
	case "hash":
		content, err = client.HGetAll(key).Result()
		size, _ = client.HLen(key).Result()
		content = parseHashContent(content.(map[string]string))
	case "list":
		content, err = client.LRange(key, 0, -1).Result()
		size, _ = client.LLen(key).Result()
	case "set":
		content, err = client.SMembers(key).Result()
		size, _ = client.SCard(key).Result()
	case "zset":
		content, err = client.ZRangeWithScores(key, 0, -1).Result()
		size, _ = client.ZCard(key).Result()
	default:
		content = "unknown type " + valType
	}

	if err != nil {
		errorMessage = err.Error()
	}

	return &ContentResult{
		Exists:   true,
		Content:  content,
		Ttl:      ttl.String(),
		Encoding: encoding,
		Size:     size,
		Error:    errorMessage,
		Format:   format,
		Type:     valType,
	}
}
func parseHashContent(m map[string]string) map[string]string {
	converted := make(map[string]string, len(m))
	for k, v := range m {
		ck := convertString(k)
		cv := convertString(v)
		converted[ck] = cv
	}

	return converted
}

func convertString(s string) string {
	if s == "" || isPrintable(s) {
		return s
	}

	quote := strconv.Quote(s)
	return quote[1 : len(quote)-1]
}

var re = regexp.MustCompile(`\\x(..)`)

func parseStringFormat(s string) (string, string) {
	if s == "" {
		return s, "UNKNOWN"
	}

	if isJSON(s) {
		return jsonPrettyPrint(s), "JSON"
	}

	if isPrintable(s) {
		return s, "NORMAL"
	}

	quote := strconv.Quote(s)
	quote = re.ReplaceAllString(quote, `$1 `)
	return quote[1 : len(quote)-1], "UNKNOWN"
}

func isJSON(s string) bool {
	var js interface{}
	return json.Unmarshal([]byte(s), &js) == nil && s != "" && (s[0] == '{' || s[0] == '[')
}

func isPrintable(s string) bool {
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func jsonPrettyPrint(in string) string {
	var out bytes.Buffer
	err := json.Indent(&out, []byte(in), "", "\t")
	if err != nil {
		return in
	}
	return out.String()
}

type KeysResult struct {
	Key  string
	Type string
	Len  int64
}

func listKeys(server RedisServer, cursor uint64, matchPattern string, maxKeys int) ([]KeysResult, uint64, error) {
	client := newRedisClient(server)
	defer client.Close()

	allKeys := make([]KeysResult, 0)
	var keys []string
	ncursor := cursor
	var err error

	for {
		keys, ncursor, err = client.Scan(ncursor, matchPattern, 10).Result()
		if err != nil {
			return nil, ncursor, err
		}

		for _, key := range keys {
			valType, err := client.Type(key).Result()
			if err != nil {
				return nil, ncursor, err
			}

			var conentLen int64
			switch valType {
			case "string":
				conentLen, _ = client.StrLen(key).Result()
			case "list":
				conentLen, _ = client.LLen(key).Result()
			case "hash":
				conentLen, _ = client.HLen(key).Result()
			case "set":
				conentLen, _ = client.SCard(key).Result()
			case "zset":
				conentLen, _ = client.ZCard(key).Result()
			default:
				conentLen = -1
			}

			allKeys = append(allKeys, KeysResult{Key: key, Type: valType, Len: conentLen})
		}

		if ncursor == 0 || (maxKeys > 0 && len(allKeys) >= maxKeys) {
			break
		}
	}

	return allKeys, ncursor, nil
}
