package gocosmos

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btnguyen2k/consu/gjrc"
	"github.com/btnguyen2k/consu/reddo"
	"github.com/btnguyen2k/consu/semita"
)

const (
	settingEndpoint           = "ACCOUNTENDPOINT"
	settingAccountKey         = "ACCOUNTKEY"
	settingTimeout            = "TIMEOUTMS"
	settingVersion            = "VERSION"
	settingAutoId             = "AUTOID"
	settingInsecureSkipVerify = "INSECURESKIPVERIFY"
	defaultApiVersion         = "2018-12-31"
)

// NewRestClient constructs a new RestClient instance from the supplied connection string.
//
// httpClient is reused if supplied. Otherwise, a new http.Client instance is created.
// connStr is expected to be in the following format:
//
//	AccountEndpoint=<cosmosdb-restapi-endpoint>;AccountKey=<account-key>[;TimeoutMs=<timeout-in-ms>][;Version=<cosmosdb-api-version>][;AutoId=<true/false>][;InsecureSkipVerify=<true/false>]
//
// If not supplied, default value for TimeoutMs is 10 seconds, Version is defaultApiVersion (which is "2018-12-31"), AutoId is true, and InsecureSkipVerify is false
//
// - AutoId is added since v0.1.2
// - InsecureSkipVerify is added since v0.1.4
func NewRestClient(httpClient *http.Client, connStr string) (*RestClient, error) {
	params := make(map[string]string)
	parts := strings.Split(connStr, ";")
	for _, part := range parts {
		tokens := strings.SplitN(part, "=", 2)
		key := strings.ToUpper(strings.TrimSpace(tokens[0]))
		if len(tokens) == 2 {
			params[key] = strings.TrimSpace(tokens[1])
		} else {
			params[key] = ""
		}
	}
	endpoint := strings.TrimSuffix(params[settingEndpoint], "/")
	if endpoint == "" {
		return nil, errors.New("AccountEndpoint not found in connection string")
	}
	accountKey := params[settingAccountKey]
	if accountKey == "" {
		return nil, errors.New("AccountKey not found in connection string")
	}
	key, err := base64.StdEncoding.DecodeString(accountKey)
	if err != nil {
		return nil, fmt.Errorf("cannot base64 decode account key: %s", err)
	}
	timeoutMs, err := strconv.Atoi(params[settingTimeout])
	if err != nil || timeoutMs < 0 {
		timeoutMs = 10000
	}
	apiVersion := params[settingVersion]
	if apiVersion == "" {
		apiVersion = defaultApiVersion
	}
	autoId, err := strconv.ParseBool(params[settingAutoId])
	if err != nil {
		autoId = true
	}
	insecureSkipVerify, err := strconv.ParseBool(params[settingInsecureSkipVerify])
	if err != nil {
		insecureSkipVerify = false
	}
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout:   time.Duration(timeoutMs) * time.Millisecond,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: insecureSkipVerify}},
		}
	}
	return &RestClient{
		client:     gjrc.NewGjrc(httpClient, time.Duration(timeoutMs)*time.Millisecond),
		endpoint:   endpoint,
		authKey:    key,
		apiVersion: apiVersion,
		autoId:     autoId,
		params:     params,
	}, nil
}

// RestClient is REST-based client for Azure CosmosDB
type RestClient struct {
	client     *gjrc.Gjrc
	endpoint   string            // Azure CosmosDB endpoint
	authKey    []byte            // Account key to authenticate
	apiVersion string            // Azure CosmosDB API version
	autoId     bool              // if true and value for 'id' field is not specified, CreateDocument
	params     map[string]string // parsed parameters
}

func (c *RestClient) buildJsonRequest(method, url string, params interface{}) *http.Request {
	var r *bytes.Reader
	if params != nil {
		js, _ := json.Marshal(params)
		r = bytes.NewReader(js)
	} else {
		r = bytes.NewReader([]byte{})
	}
	req, _ := http.NewRequest(method, url, r)
	req.Header.Set(httpHeaderContentType, "application/json")
	req.Header.Set(httpHeaderAccept, "application/json")
	req.Header.Set(restApiHeaderVersion, c.apiVersion)
	return req
}

func (c *RestClient) addAuthHeader(req *http.Request, method, resType, resId string) *http.Request {
	now := time.Now().In(locGmt)
	/*
	 * M.A.I. 2022-02-16
	 * The original statement had a single ToLower. In the resulting string the resId gets lowered when from MS Docs it should be left unaltered
	 * I came across an error on a collection with a mixed case name...
	 * stringToSign := strings.ToLower(fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n", method, resType, resId, now.Format(time.RFC1123), ""))
	 */
	stringToSign := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n", strings.ToLower(method), strings.ToLower(resType), resId, strings.ToLower(now.Format(time.RFC1123)), "")
	h := hmac.New(sha256.New, c.authKey)
	h.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(h.Sum(nil))
	authHeader := "type=master&ver=1.0&sig=" + signature
	authHeader = url.QueryEscape(authHeader)
	req.Header.Set(httpHeaderAuthorization, authHeader)
	req.Header.Set(restApiHeaderDate, now.Format(time.RFC1123))
	return req
}

func (c *RestClient) buildRestReponse(resp *gjrc.GjrcResponse) RestReponse {
	result := RestReponse{CallErr: resp.Error()}
	if result.CallErr == nil {
		result.StatusCode = resp.StatusCode()
		result.RespBody, _ = resp.Body()
		result.RespHeader = make(map[string]string)
		for k, v := range resp.HttpResponse().Header {
			if len(v) > 0 {
				// result.RespHeader[k] = v[0]
				result.RespHeader[strings.ToUpper(k)] = v[0]
			}
		}
		if v, err := strconv.ParseFloat(result.RespHeader[respHeaderRequestCharge], 64); err == nil {
			result.RequestCharge = v
		} else {
			result.RequestCharge = -1
		}
		result.SessionToken = result.RespHeader[respHeaderSessionToken]
		if result.StatusCode >= 400 {
			result.ApiErr = fmt.Errorf("error executing Azure CosmosDB command; StatusCode=%d;Body=%s", result.StatusCode, result.RespBody)
		}
	}
	return result
}

// DatabaseSpec specifies a CosmosDB database specifications for creation.
type DatabaseSpec struct {
	Id        string
	Ru, MaxRu int
}

// CreateDatabase invokes CosmosDB API to create a new database.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/create-a-database.
//
// Note: ru and maxru must not be supplied together!
func (c *RestClient) CreateDatabase(spec DatabaseSpec) *RespCreateDb {
	method := "POST"
	url := c.endpoint + "/dbs"
	req := c.buildJsonRequest(method, url, map[string]interface{}{"id": spec.Id})
	req = c.addAuthHeader(req, method, "dbs", "")
	if spec.Ru > 0 {
		req.Header.Set(restApiHeaderOfferThroughput, strconv.Itoa(spec.Ru))
	}
	if spec.MaxRu > 0 {
		req.Header.Set(restApiHeaderOfferAutopilotSettings, fmt.Sprintf(`{"maxThroughput":%d}`, spec.MaxRu))
	}

	resp := c.client.Do(req)
	result := &RespCreateDb{RestReponse: c.buildRestReponse(resp), DbInfo: DbInfo{Id: spec.Id}}
	if result.CallErr == nil {
		result.CallErr = json.Unmarshal(result.RespBody, &(result.DbInfo))
	}
	return result
}

// GetDatabase invokes CosmosDB API to get an existing database.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/get-a-database.
func (c *RestClient) GetDatabase(dbName string) *RespGetDb {
	method := "GET"
	url := c.endpoint + "/dbs/" + dbName
	req := c.buildJsonRequest(method, url, nil)
	req = c.addAuthHeader(req, method, "dbs", "dbs/"+dbName)

	resp := c.client.Do(req)
	result := &RespGetDb{RestReponse: c.buildRestReponse(resp)}
	if result.CallErr == nil {
		result.CallErr = json.Unmarshal(result.RespBody, &(result.DbInfo))
	}
	return result
}

// DeleteDatabase invokes CosmosDB API to delete an existing database.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/delete-a-database.
func (c *RestClient) DeleteDatabase(dbName string) *RespDeleteDb {
	method := "DELETE"
	url := c.endpoint + "/dbs/" + dbName
	req := c.buildJsonRequest(method, url, nil)
	req = c.addAuthHeader(req, method, "dbs", "dbs/"+dbName)

	resp := c.client.Do(req)
	result := &RespDeleteDb{RestReponse: c.buildRestReponse(resp)}
	return result
}

// ListDatabases invokes CosmosDB API to list all available databases.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/list-databases.
func (c *RestClient) ListDatabases() *RespListDb {
	method := "GET"
	url := c.endpoint + "/dbs"
	req := c.buildJsonRequest(method, url, nil)
	req = c.addAuthHeader(req, method, "dbs", "")

	resp := c.client.Do(req)
	result := &RespListDb{RestReponse: c.buildRestReponse(resp)}
	if result.CallErr == nil {
		result.CallErr = json.Unmarshal(result.RespBody, &result)
		if result.CallErr == nil {
			sort.Slice(result.Databases, func(i, j int) bool {
				// sort databases by id
				return result.Databases[i].Id < result.Databases[j].Id
			})
		}
	}
	return result
}

// CollectionSpec specifies a CosmosDB collection specifications for creation.
type CollectionSpec struct {
	DbName, CollName string
	Ru, MaxRu        int
	// PartitionKeyInfo specifies the collection's partition key.
	// At the minimum, the partition key info is a map: {paths:[/path],"kind":"Hash"}
	// If partition key is larger than 100 bytes, specify {"Version":2}
	PartitionKeyInfo map[string]interface{}
	IndexingPolicy   map[string]interface{}
	UniqueKeyPolicy  map[string]interface{}
}

// CreateCollection invokes CosmosDB API to create a new collection.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/create-a-collection.
//
// Note: ru and maxru must not be supplied together!
func (c *RestClient) CreateCollection(spec CollectionSpec) *RespCreateColl {
	method := "POST"
	url := c.endpoint + "/dbs/" + spec.DbName + "/colls"
	params := map[string]interface{}{"id": spec.CollName, "partitionKey": spec.PartitionKeyInfo}
	if spec.IndexingPolicy != nil {
		params[restApiParamIndexingPolicy] = spec.IndexingPolicy
	}
	if spec.UniqueKeyPolicy != nil {
		params[restApiParamUniqueKeyPolicy] = spec.UniqueKeyPolicy
	}
	req := c.buildJsonRequest(method, url, params)
	req = c.addAuthHeader(req, method, "colls", "dbs/"+spec.DbName)
	if spec.Ru > 0 {
		req.Header.Set(restApiHeaderOfferThroughput, strconv.Itoa(spec.Ru))
	}
	if spec.MaxRu > 0 {
		req.Header.Set(restApiHeaderOfferAutopilotSettings, fmt.Sprintf(`{"maxThroughput":%d}`, spec.MaxRu))
	}

	resp := c.client.Do(req)
	result := &RespCreateColl{RestReponse: c.buildRestReponse(resp), CollInfo: CollInfo{Id: spec.CollName}}
	if result.CallErr == nil {
		result.CallErr = json.Unmarshal(result.RespBody, &(result.CollInfo))
	}
	return result
}

// ReplaceCollection invokes CosmosDB API to replace an existing collection.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/replace-a-collection.
//
// Note: ru and maxru must not be supplied together!
func (c *RestClient) ReplaceCollection(spec CollectionSpec) *RespReplaceColl {
	method := "PUT"
	url := c.endpoint + "/dbs/" + spec.DbName + "/colls/" + spec.CollName
	params := map[string]interface{}{"id": spec.CollName}
	if spec.PartitionKeyInfo != nil {
		params[restApiParamPartitionKey] = spec.PartitionKeyInfo
	}
	if spec.IndexingPolicy != nil {
		params[restApiParamIndexingPolicy] = spec.IndexingPolicy
	}
	// The unique index cannot be modified. To change the unique index, remove the collection and re-create a new one.
	// if spec.UniqueKeyPolicy != nil {
	// 	params[restApiParamUniqueKeyPolicy] = spec.UniqueKeyPolicy
	// }
	req := c.buildJsonRequest(method, url, params)
	req = c.addAuthHeader(req, method, "colls", "dbs/"+spec.DbName+"/colls/"+spec.CollName)
	if spec.Ru > 0 {
		req.Header.Set(restApiHeaderOfferThroughput, strconv.Itoa(spec.Ru))
	}
	if spec.MaxRu > 0 {
		req.Header.Set(restApiHeaderOfferAutopilotSettings, fmt.Sprintf(`{"maxThroughput":%d}`, spec.MaxRu))
	}

	resp := c.client.Do(req)
	result := &RespReplaceColl{RestReponse: c.buildRestReponse(resp), CollInfo: CollInfo{Id: spec.CollName}}
	if result.CallErr == nil {
		result.CallErr = json.Unmarshal(result.RespBody, &(result.CollInfo))
	}
	return result
}

// GetCollection invokes CosmosDB API to get an existing collection.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/get-a-collection
func (c *RestClient) GetCollection(dbName, collName string) *RespGetColl {
	method := "GET"
	url := c.endpoint + "/dbs/" + dbName + "/colls/" + collName
	req := c.buildJsonRequest(method, url, nil)
	req = c.addAuthHeader(req, method, "colls", "dbs/"+dbName+"/colls/"+collName)

	resp := c.client.Do(req)
	result := &RespGetColl{RestReponse: c.buildRestReponse(resp)}
	if result.CallErr == nil {
		result.CallErr = json.Unmarshal(result.RespBody, &(result.CollInfo))
	}
	return result
}

// DeleteCollection invokes CosmosDB API to delete an existing collection.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/delete-a-collection.
func (c *RestClient) DeleteCollection(dbName, collName string) *RespDeleteColl {
	method := "DELETE"
	url := c.endpoint + "/dbs/" + dbName + "/colls/" + collName
	req := c.buildJsonRequest(method, url, nil)
	req = c.addAuthHeader(req, method, "colls", "dbs/"+dbName+"/colls/"+collName)

	resp := c.client.Do(req)
	result := &RespDeleteColl{RestReponse: c.buildRestReponse(resp)}
	return result
}

// ListCollections invokes CosmosDB API to list all available collections.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/list-collections.
func (c *RestClient) ListCollections(dbName string) *RespListColl {
	method := "GET"
	url := c.endpoint + "/dbs/" + dbName + "/colls"
	req := c.buildJsonRequest(method, url, nil)
	req = c.addAuthHeader(req, method, "colls", "dbs/"+dbName)

	resp := c.client.Do(req)
	result := &RespListColl{RestReponse: c.buildRestReponse(resp)}
	if result.CallErr == nil {
		result.CallErr = json.Unmarshal(result.RespBody, &result)
		if result.CallErr == nil {
			sort.Slice(result.Collections, func(i, j int) bool {
				// sort collections by id
				return result.Collections[i].Id < result.Collections[j].Id
			})
		}
	}
	return result
}

// GetPkranges invokes CosmosDB API to retrieves the list of partition key ranges for a collection.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/get-partition-key-ranges.
//
// Available since v0.1.3
func (c *RestClient) GetPkranges(dbName, collName string) *RespGetPkranges {
	method := "GET"
	url := c.endpoint + "/dbs/" + dbName + "/colls/" + collName + "/pkranges"
	req := c.buildJsonRequest(method, url, nil)
	req = c.addAuthHeader(req, method, "pkranges", "dbs/"+dbName+"/colls/"+collName)

	resp := c.client.Do(req)
	result := &RespGetPkranges{RestReponse: c.buildRestReponse(resp)}
	if result.CallErr == nil {
		result.CallErr = json.Unmarshal(result.RespBody, &result)
	}
	return result
}

// DocumentSpec specifies a CosmosDB document specifications for creation.
type DocumentSpec struct {
	DbName, CollName   string
	IsUpsert           bool
	IndexingDirective  string // accepted value "", "Include" or "Exclude"
	PartitionKeyValues []interface{}
	DocumentData       map[string]interface{}
}

// CreateDocument invokes CosmosDB API to create a new document.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/create-a-document.
func (c *RestClient) CreateDocument(spec DocumentSpec) *RespCreateDoc {
	method := "POST"
	url := c.endpoint + "/dbs/" + spec.DbName + "/colls/" + spec.CollName + "/docs"
	if c.autoId {
		if id, ok := spec.DocumentData[docFieldId].(string); !ok || strings.TrimSpace(id) == "" {
			spec.DocumentData[docFieldId] = strings.ToLower(idGen.Id128Hex())
		}
	}
	req := c.buildJsonRequest(method, url, spec.DocumentData)
	req = c.addAuthHeader(req, method, "docs", "dbs/"+spec.DbName+"/colls/"+spec.CollName)
	if spec.IsUpsert {
		req.Header.Set(restApiHeaderIsUpsert, "true")
	}
	if spec.IndexingDirective != "" {
		req.Header.Set(restApiHeaderIndexingDirective, spec.IndexingDirective)
	}
	jsPkValues, _ := json.Marshal(spec.PartitionKeyValues)
	req.Header.Set(restApiHeaderPartitionKey, string(jsPkValues))

	resp := c.client.Do(req)
	result := &RespCreateDoc{RestReponse: c.buildRestReponse(resp)}
	if result.CallErr == nil {
		result.CallErr = json.Unmarshal(result.RespBody, &(result.DocInfo))
	}
	return result
}

// ReplaceDocument invokes CosmosDB API to replace an existing document.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/replace-a-document.
func (c *RestClient) ReplaceDocument(matchEtag string, spec DocumentSpec) *RespReplaceDoc {
	id, _ := spec.DocumentData[docFieldId].(string)
	method := "PUT"
	url := c.endpoint + "/dbs/" + spec.DbName + "/colls/" + spec.CollName + "/docs/" + id
	req := c.buildJsonRequest(method, url, spec.DocumentData)
	req = c.addAuthHeader(req, method, "docs", "dbs/"+spec.DbName+"/colls/"+spec.CollName+"/docs/"+id)
	if matchEtag != "" {
		req.Header.Set(httpHeaderIfMatch, matchEtag)
	}
	jsPkValues, _ := json.Marshal(spec.PartitionKeyValues)
	req.Header.Set(restApiHeaderPartitionKey, string(jsPkValues))

	resp := c.client.Do(req)
	result := &RespReplaceDoc{RestReponse: c.buildRestReponse(resp)}
	if result.CallErr == nil {
		result.CallErr = json.Unmarshal(result.RespBody, &(result.DocInfo))
	}
	return result
}

// DocReq specifies a document request.
type DocReq struct {
	DbName, CollName, DocId string
	PartitionKeyValues      []interface{}
	MatchEtag               string // if not empty, add "If-Match" header to request
	NotMatchEtag            string // if not empty, add "If-None-Match" header to request
	ConsistencyLevel        string // accepted values: "", "Strong", "Bounded", "Session" or "Eventual"
	SessionToken            string // string token used with session level consistency
}

// GetDocument invokes CosmosDB API to get an existing document.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/get-a-document.
func (c *RestClient) GetDocument(r DocReq) *RespGetDoc {
	method := "GET"
	url := c.endpoint + "/dbs/" + r.DbName + "/colls/" + r.CollName + "/docs/" + r.DocId
	req := c.buildJsonRequest(method, url, nil)
	req = c.addAuthHeader(req, method, "docs", "dbs/"+r.DbName+"/colls/"+r.CollName+"/docs/"+r.DocId)
	jsPkValues, _ := json.Marshal(r.PartitionKeyValues)
	req.Header.Set(restApiHeaderPartitionKey, string(jsPkValues))
	if r.NotMatchEtag != "" {
		req.Header.Set(httpHeaderIfNoneMatch, r.NotMatchEtag)
	}
	if r.ConsistencyLevel != "" {
		req.Header.Set(restApiHeaderConsistencyLevel, r.ConsistencyLevel)
	}
	if r.SessionToken != "" {
		req.Header.Set(restApiHeaderSessionToken, r.SessionToken)
	}

	resp := c.client.Do(req)
	result := &RespGetDoc{RestReponse: c.buildRestReponse(resp)}
	if result.CallErr == nil && result.StatusCode != 304 {
		result.CallErr = json.Unmarshal(result.RespBody, &(result.DocInfo))
	}
	return result
}

// DeleteDocument invokes CosmosDB API to delete an existing document.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/delete-a-document.
func (c *RestClient) DeleteDocument(r DocReq) *RespDeleteDoc {
	method := "DELETE"
	url := c.endpoint + "/dbs/" + r.DbName + "/colls/" + r.CollName + "/docs/" + r.DocId
	req := c.buildJsonRequest(method, url, nil)
	req = c.addAuthHeader(req, method, "docs", "dbs/"+r.DbName+"/colls/"+r.CollName+"/docs/"+r.DocId)
	jsPkValues, _ := json.Marshal(r.PartitionKeyValues)
	req.Header.Set(restApiHeaderPartitionKey, string(jsPkValues))
	if r.MatchEtag != "" {
		req.Header.Set(httpHeaderIfMatch, r.MatchEtag)
	}

	resp := c.client.Do(req)
	result := &RespDeleteDoc{RestReponse: c.buildRestReponse(resp)}
	return result
}

// QueryReq specifies a query request to query for documents.
type QueryReq struct {
	DbName, CollName      string
	Query                 string
	Params                []interface{}
	MaxItemCount          int    // if max-item-count = 0: use server side default value, (since v0.1.8) if max-item-count < 0: client will fetch all returned documents from server
	PkRangeId             string // (since v0.1.8) if non-empty, query will perform only on this PkRangeId (if PkRangeId and PkValue are specified, PkRangeId takes priority)
	PkValue               string // (since v0.1.8) if non-empty, query will perform only on the partition that PkValue maps to (if PkRangeId and PkValue are specified, PkRangeId takes priority)
	ContinuationToken     string
	CrossPartitionEnabled bool
	ConsistencyLevel      string // accepted values: "", "Strong", "Bounded", "Session" or "Eventual"
	SessionToken          string // string token used with session level consistency
}

// func (c *RestClient) queryDocumentsForPkRange(baseReq *http.Request, pkRangeId string) *RespQueryDocs {
// 	req := baseReq.Clone(baseReq.Context())
// 	req.Header.Set(restApiHeaderPartitionKeyRangeId, pkRangeId)
// 	var result *RespQueryDocs
// 	for {
// 		resp := c.client.Do(req)
// 		tempResult := &RespQueryDocs{RestReponse: c.buildRestReponse(resp)}
// 		if tempResult.CallErr == nil {
// 			tempResult.ContinuationToken = tempResult.RespHeader[respHeaderContinuation]
// 			tempResult.CallErr = json.Unmarshal(tempResult.RespBody, &tempResult)
// 			tempResult.Count = int64(len(tempResult.Documents))
// 		}
// 		if result != nil {
// 			tempResult.Count += result.Count
// 			tempResult.RequestCharge += result.RequestCharge
// 			tempResult.Documents = append(result.Documents, tempResult.Documents...)
// 		}
// 		result = tempResult
// 		if result.CallErr != nil || tempResult.ContinuationToken == "" {
// 			break
// 		}
// 		req.Header.Set(restApiHeaderContinuation, tempResult.ContinuationToken)
// 	}
// 	return result
// }

func (c *RestClient) buildQueryRequest(query QueryReq) *http.Request {
	method, url := "POST", c.endpoint+"/dbs/"+query.DbName+"/colls/"+query.CollName+"/docs"
	requestBody := make(map[string]interface{}, 0)
	requestBody[restApiParamQuery] = query.Query
	if query.Params != nil {
		// M.A.I. 2022-02-16: server will complain if parameter set to nil
		requestBody[restApiParamParameters] = query.Params
	}
	req := c.buildJsonRequest(method, url, requestBody)
	req = c.addAuthHeader(req, method, "docs", "dbs/"+query.DbName+"/colls/"+query.CollName)
	req.Header.Set(httpHeaderContentType, "application/query+json")
	req.Header.Set(restApiHeaderIsQuery, "true")
	req.Header.Set(restApiHeaderPopulateMetrics, "true")
	if query.MaxItemCount > 0 {
		req.Header.Set(restApiHeaderPageSize, strconv.Itoa(query.MaxItemCount))
	}
	if query.ContinuationToken != "" {
		req.Header.Set(restApiHeaderContinuation, query.ContinuationToken)
	}
	if query.ConsistencyLevel != "" {
		req.Header.Set(restApiHeaderConsistencyLevel, query.ConsistencyLevel)
	}
	if query.SessionToken != "" {
		req.Header.Set(restApiHeaderSessionToken, query.SessionToken)
	}
	if query.PkRangeId != "" {
		req.Header.Set(restApiHeaderPartitionKeyRangeId, query.PkRangeId)
	} else if query.PkValue != "" {
		req.Header.Set(restApiHeaderPartitionKey, `["`+query.PkValue+`"]`)
	}
	return req
}

// TODO
// - [x] (v0.1.7+) simple cross-partition queries (+paging)
// - [-] cross-partition queries with ordering (+paging) / partial supported if number of pkrange == 1
// - [-] cross-partition queries with group-by (+paging) / partial supported if number of pkrange == 1
func (c *RestClient) queryDocumentsCrossPartitions(query QueryReq) *RespQueryDocs {
	pkranges := c.GetPkranges(query.DbName, query.CollName)
	if pkranges.Error() != nil {
		return &RespQueryDocs{RestReponse: pkranges.RestReponse}
	}
	if pkranges.Count == 1 {
		query.PkRangeId = pkranges.Pkranges[0].Id
		return c.QueryDocuments(query)
	}

	req := c.buildQueryRequest(query)
	req.Header.Set(restApiHeaderEnableCrossPartitionQuery, "true")
	var result *RespQueryDocs
	for {
		if query.MaxItemCount < 0 {
			// request chunk by chunk as it would have negative impact if we fetch a large number of documents in one go
			req.Header.Set(restApiHeaderPageSize, "100")
		}
		resp := c.client.Do(req)
		tempResult := &RespQueryDocs{RestReponse: c.buildRestReponse(resp)}
		if tempResult.CallErr == nil {
			tempResult.ContinuationToken = tempResult.RespHeader[respHeaderContinuation]
			tempResult.CallErr = json.Unmarshal(tempResult.RespBody, &tempResult)
		}
		if result != nil {
			tempResult.Count += result.Count
			tempResult.RequestCharge += result.RequestCharge
			tempResult.Documents = append(result.Documents, tempResult.Documents...)
		}
		result = tempResult
		if result.CallErr != nil || query.MaxItemCount >= 0 || tempResult.ContinuationToken == "" {
			break
		}
		req.Header.Set(restApiHeaderContinuation, tempResult.ContinuationToken)
	}
	return result
}

// QueryDocuments invokes CosmosDB API to query a collection for documents.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/query-documents.
func (c *RestClient) QueryDocuments(query QueryReq) *RespQueryDocs {
	if query.CrossPartitionEnabled && query.PkRangeId == "" && query.PkValue == "" {
		// cross-partition is redundant when pkrangid or pkvalue is specified
		return c.queryDocumentsCrossPartitions(query)
	}
	req := c.buildQueryRequest(query)
	var result *RespQueryDocs
	for {
		if query.MaxItemCount < 0 {
			// request chunk by chunk as it would have negative impact if we fetch a large number of documents in one go
			req.Header.Set(restApiHeaderPageSize, "100")
		}
		resp := c.client.Do(req)
		tempResult := &RespQueryDocs{RestReponse: c.buildRestReponse(resp)}
		if tempResult.CallErr == nil {
			tempResult.ContinuationToken = tempResult.RespHeader[respHeaderContinuation]
			tempResult.CallErr = json.Unmarshal(tempResult.RespBody, &tempResult)
		}
		if result != nil {
			tempResult.Count += result.Count
			tempResult.RequestCharge += result.RequestCharge
			tempResult.Documents = append(result.Documents, tempResult.Documents...)
		}
		result = tempResult
		if result.CallErr != nil || query.MaxItemCount >= 0 || tempResult.ContinuationToken == "" {
			break
		}
		req.Header.Set(restApiHeaderContinuation, tempResult.ContinuationToken)
	}
	return result
}

// QueryPlan invokes CosmosDB API to generate query plan.
//
// Available since v0.1.8
func (c *RestClient) QueryPlan(query QueryReq) *RespQueryPlan {
	method, url := "POST", c.endpoint+"/dbs/"+query.DbName+"/colls/"+query.CollName+"/docs"
	requestBody := make(map[string]interface{}, 0)
	requestBody[restApiParamQuery] = query.Query
	if query.Params != nil {
		requestBody[restApiParamParameters] = query.Params
	}
	req := c.buildJsonRequest(method, url, requestBody)
	req = c.addAuthHeader(req, method, "docs", "dbs/"+query.DbName+"/colls/"+query.CollName)
	req.Header.Set(httpHeaderContentType, "application/query+json")
	if query.MaxItemCount > 0 {
		req.Header.Set(restApiHeaderPageSize, strconv.Itoa(query.MaxItemCount))
	}
	if query.ContinuationToken != "" {
		req.Header.Set(restApiHeaderContinuation, query.ContinuationToken)
	}
	if query.ConsistencyLevel != "" {
		req.Header.Set(restApiHeaderConsistencyLevel, query.ConsistencyLevel)
	}
	if query.SessionToken != "" {
		req.Header.Set(restApiHeaderSessionToken, query.SessionToken)
	}
	req.Header.Set(restApiHeaderIsQueryPlanRequest, "True") // Caution: as of Dec-2022 "true" (lower-cased "t") does not work
	req.Header.Set(restApiHeaderSupportedQueryFeatures, "NonValueAggregate, Aggregate, Distinct, MultipleOrderBy, OffsetAndLimit, OrderBy, Top, CompositeAggregate, GroupBy, MultipleAggregates")
	req.Header.Set(restApiHeaderEnableCrossPartitionQuery, "true")
	req.Header.Set(restApiHeaderParallelizeCrossPartitionQuery, "true")
	resp := c.client.Do(req)
	result := &RespQueryPlan{RestReponse: c.buildRestReponse(resp)}
	if result.CallErr == nil {
		result.CallErr = json.Unmarshal(result.RespBody, &result)
	}
	return result
}

// ListDocsReq specifies a list documents request.
type ListDocsReq struct {
	DbName, CollName    string
	MaxItemCount        int
	ContinuationToken   string
	ConsistencyLevel    string // accepted values: "", "Strong", "Bounded", "Session" or "Eventual"
	SessionToken        string // string token used with session level consistency
	NotMatchEtag        string
	PartitionKeyRangeId string
}

// ListDocuments invokes CosmosDB API to query read-feed for documents.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/list-documents.
func (c *RestClient) ListDocuments(r ListDocsReq) *RespListDocs {
	method := "GET"
	url := c.endpoint + "/dbs/" + r.DbName + "/colls/" + r.CollName + "/docs"
	req := c.buildJsonRequest(method, url, nil)
	req = c.addAuthHeader(req, method, "docs", "dbs/"+r.DbName+"/colls/"+r.CollName)
	if r.MaxItemCount > 0 {
		req.Header.Set(restApiHeaderPageSize, strconv.Itoa(r.MaxItemCount))
	}
	if r.ContinuationToken != "" {
		req.Header.Set(restApiHeaderContinuation, r.ContinuationToken)
	}
	if r.ConsistencyLevel != "" {
		req.Header.Set(restApiHeaderConsistencyLevel, r.ConsistencyLevel)
	}
	if r.SessionToken != "" {
		req.Header.Set(restApiHeaderSessionToken, r.SessionToken)
	}
	if r.NotMatchEtag != "" {
		req.Header.Set(httpHeaderIfNoneMatch, r.NotMatchEtag)
	}
	if r.PartitionKeyRangeId != "" {
		req.Header.Set(restApiHeaderPartitionKeyRangeId, r.PartitionKeyRangeId)
	}

	resp := c.client.Do(req)
	result := &RespListDocs{RestReponse: c.buildRestReponse(resp)}
	if result.CallErr == nil {
		result.ContinuationToken = result.RespHeader[respHeaderContinuation]
		result.Etag = result.RespHeader[respHeaderEtag]
		result.CallErr = json.Unmarshal(result.RespBody, &result)
	}
	return result
}

// GetOfferForResource invokes CosmosDB API to get offer info of a resource.
//
// Available since v0.1.1
func (c *RestClient) GetOfferForResource(rid string) *RespGetOffer {
	queryResult := c.QueryOffers(`SELECT * FROM root WHERE root.offerResourceId="` + rid + `"`)
	result := &RespGetOffer{RestReponse: queryResult.RestReponse}
	if result.Error() == nil {
		if len(queryResult.Offers) == 0 {
			result.StatusCode = 404
			result.ApiErr = fmt.Errorf("offer not found; StatusCode=%d;Body=%s", result.StatusCode, result.RespBody)
		} else {
			result.OfferInfo = queryResult.Offers[0]
		}
	}
	return result
}

// QueryOffers invokes CosmosDB API to query existing offers.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/querying-offers.
//
// Available since v0.1.1
func (c *RestClient) QueryOffers(query string) *RespQueryOffers {
	method := "POST"
	url := c.endpoint + "/offers"
	req := c.buildJsonRequest(method, url, map[string]interface{}{"query": query})
	req = c.addAuthHeader(req, method, "offers", "")
	req.Header.Set(httpHeaderContentType, "application/query+json")
	req.Header.Set(restApiHeaderIsQuery, "true")

	resp := c.client.Do(req)
	result := &RespQueryOffers{RestReponse: c.buildRestReponse(resp)}
	if result.CallErr == nil {
		result.ContinuationToken = result.RespHeader[respHeaderContinuation]
		result.CallErr = json.Unmarshal(result.RespBody, &result)
	}
	return result
}

func (c *RestClient) buildReplaceOfferContentAndHeaders(currentOffer OfferInfo, ru, maxru int) (map[string]interface{}, map[string]string) {
	headers := make(map[string]string)
	contentManualThroughput := map[string]interface{}{"offerThroughput": ru}
	contentDisableManualThroughput := map[string]interface{}{"offerThroughput": -1}
	contentAutopilotThroughput := map[string]interface{}{"offerAutopilotSettings": map[string]interface{}{"maxThroughput": maxru}}
	contentDisableAutopilotThroughput := map[string]interface{}{"offerAutopilotSettings": map[string]interface{}{"maxThroughput": -1}}
	if ru > 0 && maxru <= 0 {
		if currentOffer.IsAutopilot() {
			// change from auto-pilot to manual provisioning
			headers[restApiHeaderMigrateToManualThroughput] = "true"
			return contentDisableAutopilotThroughput, headers
		}
		return contentManualThroughput, headers
	}
	if ru <= 0 && maxru > 0 {
		if !currentOffer.IsAutopilot() {
			// change from manual to auto-pilot provisioning
			headers[restApiHeaderMigrateToAutopilotThroughput] = "true"
			return contentDisableManualThroughput, headers
		}
		return contentAutopilotThroughput, headers
	}
	// if we reach here, ru<=0 and maxru<=0
	if !currentOffer.IsAutopilot() {
		// change from manual to auto-pilot provisioning
		headers[restApiHeaderMigrateToAutopilotThroughput] = "true"
		return contentDisableManualThroughput, headers
	}
	return nil, headers
}

// ReplaceOfferForResource invokes CosmosDB API to replace/update offer info of a resource.
//
//   - If ru > 0 and maxru <= 0: switch to manual throughput and set provisioning value to ru.
//   - If ru <= 0 and maxru > 0: switch to autopilot throughput and set max provisioning value to maxru.
//   - If ru <= 0 and maxru <= 0: switch to autopilot throughput with default provisioning value.
//
// Available since v0.1.1
func (c *RestClient) ReplaceOfferForResource(rid string, ru, maxru int) *RespReplaceOffer {
	if ru > 0 && maxru > 0 {
		return &RespReplaceOffer{
			RestReponse: RestReponse{
				ApiErr:     errors.New("either one of RU or MAXRU must be supplied, not both"),
				StatusCode: 400,
			},
		}
	}

	getResult := c.GetOfferForResource(rid)
	if getResult.Error() == nil {
		method := "PUT"
		url := c.endpoint + "/offers/" + getResult.OfferInfo.Rid
		params := map[string]interface{}{
			"offerVersion": "V2", "offerType": "Invalid",
			"resource":        getResult.OfferInfo.Resource,
			"offerResourceId": getResult.OfferInfo.OfferResourceId,
			"id":              getResult.OfferInfo.Rid,
			"_rid":            getResult.OfferInfo.Rid,
		}
		content, headers := c.buildReplaceOfferContentAndHeaders(getResult.OfferInfo, ru, maxru)
		if content == nil {
			return &RespReplaceOffer{RestReponse: getResult.RestReponse, OfferInfo: getResult.OfferInfo}
		}
		params[restApiParamContent] = content
		req := c.buildJsonRequest(method, url, params)
		/*
		 * [btnguyen2k] 2022-02-16
		 * OfferInfo.Rid is returned from the server, but it _must_ be lower-cased when we send back to the server for
		 * issuing the 'replace-offer' request.
		 * Not sure if this is intended or a bug of CosmosDB.
		 */
		req = c.addAuthHeader(req, method, "offers", strings.ToLower(getResult.OfferInfo.Rid))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp := c.client.Do(req)
		result := &RespReplaceOffer{RestReponse: c.buildRestReponse(resp)}
		if result.CallErr == nil {
			if (headers[restApiHeaderMigrateToAutopilotThroughput] == "true" && maxru > 0) || (headers[restApiHeaderMigrateToManualThroughput] == "true" && ru > 0) {
				return c.ReplaceOfferForResource(rid, ru, maxru)
			}
			result.CallErr = json.Unmarshal(result.RespBody, &result.OfferInfo)
		}
		return result
	}
	return &RespReplaceOffer{RestReponse: getResult.RestReponse}
}

/*----------------------------------------------------------------------*/

// RestReponse captures the response from REST API call.
type RestReponse struct {
	// CallErr holds any error occurred during the REST call.
	CallErr error
	// ApiErr holds any error occurred during the API call (only available when StatusCode >= 400).
	ApiErr error
	// StatusCode captures the HTTP status code from the REST call.
	StatusCode int
	// RespBody captures the body response from the REST call.
	RespBody []byte
	// RespHeader captures the header response from the REST call.
	RespHeader map[string]string
	// RequestCharge is number of request units consumed by the operation
	RequestCharge float64
	// SessionToken is used with session level consistency. Clients must save this value and set it for subsequent read requests for session consistency.
	SessionToken string
}

// Error returns CallErr if not nil, ApiErr otherwise.
func (r RestReponse) Error() error {
	if r.CallErr != nil {
		return r.CallErr
	}
	return r.ApiErr
}

// DbInfo captures info of a CosmosDB database.
type DbInfo struct {
	Id    string `json:"id"`     // user-generated unique name for the database
	Rid   string `json:"_rid"`   // (system generated property) _rid attribute of the database
	Ts    int64  `json:"_ts"`    // (system-generated property) _ts attribute of the database
	Self  string `json:"_self"`  // (system-generated property) _self attribute of the database
	Etag  string `json:"_etag"`  // (system-generated property) _etag attribute of the database
	Colls string `json:"_colls"` // (system-generated property) _colls attribute of the database
	Users string `json:"_users"` // (system-generated property) _users attribute of the database
}

// RespCreateDb captures the response from CreateDatabase call.
type RespCreateDb struct {
	RestReponse
	DbInfo
}

// RespGetDb captures the response from GetDatabase call.
type RespGetDb struct {
	RestReponse
	DbInfo
}

// RespDeleteDb captures the response from DeleteDatabase call.
type RespDeleteDb struct {
	RestReponse
}

// RespListDb captures the response from ListDatabases call.
type RespListDb struct {
	RestReponse `json:"-"`
	Count       int      `json:"_count"` // number of databases returned from the list operation
	Databases   []DbInfo `json:"Databases"`
}

// CollInfo captures info of a CosmosDB collection.
type CollInfo struct {
	Id                       string                 `json:"id"`                       // user-generated unique name for the collection
	Rid                      string                 `json:"_rid"`                     // (system generated property) _rid attribute of the collection
	Ts                       int64                  `json:"_ts"`                      // (system-generated property) _ts attribute of the collection
	Self                     string                 `json:"_self"`                    // (system-generated property) _self attribute of the collection
	Etag                     string                 `json:"_etag"`                    // (system-generated property) _etag attribute of the collection
	Docs                     string                 `json:"_docs"`                    // (system-generated property) _docs attribute of the collection
	Sprocs                   string                 `json:"_sprocs"`                  // (system-generated property) _sprocs attribute of the collection
	Triggers                 string                 `json:"_triggers"`                // (system-generated property) _triggers attribute of the collection
	Udfs                     string                 `json:"_udfs"`                    // (system-generated property) _udfs attribute of the collection
	Conflicts                string                 `json:"_conflicts"`               // (system-generated property) _conflicts attribute of the collection
	IndexingPolicy           map[string]interface{} `json:"indexingPolicy"`           // indexing policy settings for collection
	PartitionKey             map[string]interface{} `json:"partitionKey"`             // partitioning configuration settings for collection
	ConflictResolutionPolicy map[string]interface{} `json:"conflictResolutionPolicy"` // conflict resolution policy settings for collection
	GeospatialConfig         map[string]interface{} `json:"geospatialConfig"`         // Geo-spatial configuration settings for collection
}

// RespCreateColl captures the response from CreateCollection call.
type RespCreateColl struct {
	RestReponse
	CollInfo
}

// RespReplaceColl captures the response from ReplaceCollection call.
type RespReplaceColl struct {
	RestReponse
	CollInfo
}

// RespGetColl captures the response from GetCollection call.
type RespGetColl struct {
	RestReponse
	CollInfo
}

// RespDeleteColl captures the response from DeleteCollection call.
type RespDeleteColl struct {
	RestReponse
}

// RespListColl captures the response from ListCollections call.
type RespListColl struct {
	RestReponse `json:"-"`
	Count       int        `json:"_count"` // number of collections returned from the list operation
	Collections []CollInfo `json:"DocumentCollections"`
}

// DocInfo captures info of a CosmosDB document.
type DocInfo map[string]interface{}

// RemoveSystemAttrs returns a clone of the document with all system attributes removed.
func (d DocInfo) RemoveSystemAttrs() DocInfo {
	clone := DocInfo{}
	for k, v := range d {
		if !strings.HasPrefix(k, "_") {
			clone[k] = v
		}
	}
	return clone
}

// GetAttrAsType returns a document attribute converting to a specific type.
//
// Note: if typ is nil, the attribute value is returned as-is (i.e. without converting).
func (d DocInfo) GetAttrAsType(attrName string, typ reflect.Type) (interface{}, error) {
	v, ok := d[attrName]
	if ok && v != nil {
		return reddo.Convert(v, typ)
	}
	return nil, nil
}

// Id returns the value of document's "id" attribute.
func (d DocInfo) Id() string {
	v := d.GetAttrAsTypeUnsafe("id", reddo.TypeString)
	if v != nil {
		return v.(string)
	}
	return ""
}

// Rid returns the value of document's "_rid" attribute.
func (d DocInfo) Rid() string {
	v := d.GetAttrAsTypeUnsafe("_rid", reddo.TypeString)
	if v != nil {
		return v.(string)
	}
	return ""
}

// Attachments returns the value of document's "_attachments" attribute.
func (d DocInfo) Attachments() string {
	v := d.GetAttrAsTypeUnsafe("_attachments", reddo.TypeString)
	if v != nil {
		return v.(string)
	}
	return ""
}

// Etag returns the value of document's "_etag" attribute.
func (d DocInfo) Etag() string {
	v := d.GetAttrAsTypeUnsafe("_etag", reddo.TypeString)
	if v != nil {
		return v.(string)
	}
	return ""
}

// Self returns the value of document's "_self" attribute.
func (d DocInfo) Self() string {
	v := d.GetAttrAsTypeUnsafe("_self", reddo.TypeString)
	if v != nil {
		return v.(string)
	}
	return ""
}

// Ts returns the value of document's "_ts" attribute.
func (d DocInfo) Ts() int64 {
	v := d.GetAttrAsTypeUnsafe("_ts", reddo.TypeInt)
	if v != nil {
		return v.(int64)
	}
	return 0
}

// TsAsTime returns the value of document's "_ts" attribute as a time.Time.
func (d DocInfo) TsAsTime() time.Time {
	return time.Unix(d.Ts(), 0)
}

// GetAttrAsTypeUnsafe is similar to GetAttrAsType except that it does not check for error.
func (d DocInfo) GetAttrAsTypeUnsafe(attrName string, typ reflect.Type) interface{} {
	v, _ := d.GetAttrAsType(attrName, typ)
	return v
}

// RespCreateDoc captures the response from CreateDocument call.
type RespCreateDoc struct {
	RestReponse
	DocInfo
}

// RespReplaceDoc captures the response from ReplaceDocument call.
type RespReplaceDoc struct {
	RestReponse
	DocInfo
}

// RespGetDoc captures the response from GetDocument call.
type RespGetDoc struct {
	RestReponse
	DocInfo
}

// RespDeleteDoc captures the response from DeleteDocument call.
type RespDeleteDoc struct {
	RestReponse
}

// RespQueryDocs captures the response from QueryDocuments call.
type RespQueryDocs struct {
	RestReponse       `json:"-"`
	Count             int       `json:"_count"` // number of documents returned from the operation
	Documents         []DocInfo `json:"Documents"`
	ContinuationToken string    `json:"-"`
}

type typDistinctType string // possible values: None, Ordered, Unordered
type typOrderBy string      // possible values: Ascending, Descending
type typAggregates string   // possible values: Average, Count, Max, Min, Sum
type typDCountInfo struct {
	DCountAlias string `json:"dCountAlias"`
}

// RespQueryPlan captures the response from QueryPlan call.
//
// Available since v0.1.8
type RespQueryPlan struct {
	RestReponse               `json:"-"`
	QueryExecutionInfoVersion int `json:"partitionedQueryExecutionInfoVersion"`
	QueryInfo                 struct {
		DistinctType                typDistinctType          `json:"distinctType"`
		Top                         int                      `json:"top"`
		Offset                      int                      `json:"offset"`
		Limit                       int                      `json:"limit"`
		OrderBy                     []typOrderBy             `json:"orderBy"`
		OrderByExpressions          []string                 `json:"orderByExpressions"`
		GroupByExpressions          []string                 `json:"groupByExpressions"`
		GroupByAliases              []string                 `json:"groupByAliases"`
		Aggregates                  []typAggregates          `json:"aggregates"`
		GroupByAliasToAggregateType map[string]typAggregates `json:"groupByAliasToAggregateType"`
		RewrittenQuery              string                   `json:"rewrittenQuery"`
		HasSelectValue              bool                     `json:"hasSelectValue"`
		DCountInfo                  typDCountInfo            `json:"dCountInfo"`
	} `json:"queryInfo"`
}

// RespListDocs captures the response from ListDocuments call.
type RespListDocs struct {
	RestReponse       `json:"-"`
	Count             int       `json:"_count"` // number of documents returned from the operation
	Documents         []DocInfo `json:"Documents"`
	ContinuationToken string    `json:"-"`
	Etag              string    `json:"-"` // logical sequence number (LSN) of last document returned in the response
}

// OfferInfo captures info of a CosmosDB offer.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/offers.
type OfferInfo struct {
	OfferVersion    string                 `json:"offerVersion"`    // V2 is the current version for request unit-based throughput.
	OfferType       string                 `json:"offerType"`       // This value indicates the performance level for V1 offer version, allowed values for V1 offer are S1, S2, or S3. This property is set to Invalid for V2 offer version.
	Content         map[string]interface{} `json:"content"`         // Contains information about the offer – for V2 offers, this contains the throughput of the collection.
	Resource        string                 `json:"resource"`        // When creating a new collection, this property is set to the self-link of the collection.
	OfferResourceId string                 `json:"offerResourceId"` // During creation of a collection, this property is automatically associated to the resource ID, that is, _rid of the collection.
	Id              string                 `json:"id"`              // It is a system-generated property. The ID for the offer resource is automatically generated when it is created. It has the same value as the _rid for the offer.
	Rid             string                 `json:"_rid"`            // It is a system-generated property. The resource ID (_rid) is a unique identifier that is also hierarchical per the resource stack on the resource model. It is used internally for placement and navigation of the offer.
	Ts              int64                  `json:"_ts"`             // It is a system-generated property. It specifies the last updated timestamp of the resource. The value is a timestamp.
	Self            string                 `json:"_self"`           // It is a system-generated property. It is the unique addressable URI for the resource.
	Etag            string                 `json:"_etag"`           // It is a system-generated property that specifies the resource etag required for optimistic concurrency control.
	_lock           sync.Mutex
	_s              *semita.Semita
}

// OfferThroughput returns value of field 'offerThroughput'
func (o OfferInfo) OfferThroughput() int {
	o._lock.Lock()
	if o._s == nil {
		o._s = semita.NewSemita(o.Content)
	}
	defer o._lock.Unlock()
	v, err := o._s.GetValueOfType("offerThroughput", reddo.TypeInt)
	if err == nil {
		return int(v.(int64))
	}
	return 0
}

// MaxThroughputEverProvisioned returns value of field 'maxThroughputEverProvisioned'
func (o OfferInfo) MaxThroughputEverProvisioned() int {
	o._lock.Lock()
	if o._s == nil {
		o._s = semita.NewSemita(o.Content)
	}
	defer o._lock.Unlock()
	v, err := o._s.GetValueOfType("offerMinimumThroughputParameters.maxThroughputEverProvisioned", reddo.TypeInt)
	if err == nil {
		return int(v.(int64))
	}
	return 0
}

// IsAutopilot returns true if auto pilot is enabled, false otherwise.
func (o OfferInfo) IsAutopilot() bool {
	o._lock.Lock()
	if o._s == nil {
		o._s = semita.NewSemita(o.Content)
	}
	defer o._lock.Unlock()
	v, err := o._s.GetValue("offerAutopilotSettings")
	return err == nil && v != nil
}

// RespGetOffer captures the response from GetOffer call.
type RespGetOffer struct {
	RestReponse
	OfferInfo
}

// RespQueryOffers captures the response from QueryOffers call.
type RespQueryOffers struct {
	RestReponse       `json:"-"`
	Count             int         `json:"_count"` // number of records returned from the operation
	Offers            []OfferInfo `json:"Offers"`
	ContinuationToken string      `json:"-"`
}

// RespReplaceOffer captures the response from ReplaceOffer call.
type RespReplaceOffer struct {
	RestReponse
	OfferInfo
}

// PkrangeInfo captures info of a collection's partition key range.
//
// See: https://docs.microsoft.com/en-us/rest/api/cosmos-db/get-partition-key-ranges.
//
// Available since v0.1.3.
type PkrangeInfo struct {
	Id           string `json:"id"`           // the stable and unique ID for the partition key range within each collection
	MaxExclusive string `json:"maxExclusive"` // (internal use) the maximum partition key hash value for the partition key range
	MinInclusive string `json:"minInclusive"` // (minimum use) the maximum partition key hash value for the partition key range
	Rid          string `json:"_rid"`         // (system generated property) _rid attribute of the pkrange
	Ts           int64  `json:"_ts"`          // (system-generated property) _ts attribute of the pkrange
	Self         string `json:"_self"`        // (system-generated property) _self attribute of the pkrange
	Etag         string `json:"_etag"`        // (system-generated property) _etag attribute of the pkrange
}

// RespGetPkranges captures the response from GetPkranges call.
//
// Available since v0.1.3.
type RespGetPkranges struct {
	RestReponse `json:"-"`
	Pkranges    []PkrangeInfo `json:"PartitionKeyRanges"`
	Count       int           `json:"_count"` // number of records returned from the operation
}
