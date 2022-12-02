package gocosmos

const (
	httpHeaderContentType   = "Content-Type"
	httpHeaderAccept        = "Accept"
	httpHeaderAuthorization = "Authorization"
	httpHeaderIfMatch       = "If-Match"
	httpHeaderIfNoneMatch   = "If-None-Match"

	restApiHeaderVersion                        = "X-Ms-Version"
	restApiHeaderDate                           = "X-Ms-Date"
	restApiHeaderOfferThroughput                = "X-Ms-Offer-Throughput"
	restApiHeaderOfferAutopilotSettings         = "X-Ms-Cosmos-Offer-Autopilot-Settings"
	restApiHeaderIsUpsert                       = "X-Ms-Documentdb-Is-Upsert"
	restApiHeaderIndexingDirective              = "X-Ms-Indexing-Directive"
	restApiHeaderPartitionKey                   = "X-Ms-Documentdb-PartitionKey"
	restApiHeaderPartitionKeyRangeId            = "X-Ms-Documentdb-PartitionKeyRangeId"
	restApiHeaderConsistencyLevel               = "X-Ms-Consistency-Level"
	restApiHeaderSessionToken                   = "X-Ms-Session-Token"
	restApiHeaderContinuation                   = "X-Ms-Continuation"
	restApiHeaderPageSize                       = "X-Ms-Max-Item-Count"
	restApiHeaderEnableCrossPartitionQuery      = "X-Ms-Documentdb-Query-EnableCrossPartition"
	restApiHeaderParallelizeCrossPartitionQuery = "X-Ms-Documentdb-Query-ParallelizeCrossPartitionQuery"
	restApiHeaderIsQuery                        = "X-Ms-Documentdb-Isquery"
	restApiHeaderMigrateToManualThroughput      = "X-Ms-Cosmos-Migrate-Offer-To-Manual-Throughput"
	restApiHeaderMigrateToAutopilotThroughput   = "X-Ms-Cosmos-Migrate-Offer-To-Autopilot"

	restApiParamIndexingPolicy  = "indexingPolicy"
	restApiParamUniqueKeyPolicy = "uniqueKeyPolicy"
	restApiParamPartitionKey    = "partitionKey"
	restApiParamQuery           = "query"
	restApiParamParameters      = "parameters"
	restApiParamContent         = "content"

	respHeaderRequestCharge = "X-MS-REQUEST-CHARGE"
	respHeaderSessionToken  = "X-MS-SESSION-TOKEN"
	respHeaderContinuation  = "X-MS-CONTINUATION"
	respHeaderEtag          = "ETAG"

	docFieldId = "id"
)
