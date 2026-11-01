package mongodb

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/Tangerg/scope/core/vectorstore"
)

// Compute the cumulative BSON size on the server. An over-budget document
// contributes only a sentinel, never its payload; a cursor cannot certify a
// partial result as complete. Ranking is a separate read of selected IDs.
func scanPipeline(vectorPath string, budget vectorstore.ScanBudget) mongo.Pipeline {
	fits := bson.M{"$and": bson.A{bson.M{"$lte": bson.A{"$ordinal", int64(budget.MaxRecords)}}, bson.M{"$lte": bson.A{"$bytes", budget.MaxBytes}}}}
	return mongo.Pipeline{
		{{Key: "$match", Value: bson.M{vectorPath: bson.M{"$exists": true}}}},
		{{Key: "$limit", Value: int64(budget.MaxRecords) + 1}},
		{{Key: "$project", Value: bson.M{"_id": 1, "document": "$$ROOT", "size": bson.M{"$bsonSize": "$$ROOT"}}}},
		{{Key: "$setWindowFields", Value: bson.M{"sortBy": bson.D{{Key: "_id", Value: 1}}, "output": bson.M{"ordinal": bson.M{"$documentNumber": bson.M{}}, "bytes": bson.M{"$sum": "$size", "window": bson.M{"documents": bson.A{"unbounded", "current"}}}}}}},
		{{Key: "$project", Value: bson.M{"_id": 0, "within": fits, "document": bson.M{"$cond": bson.A{fits, "$document", "$$REMOVE"}}}}},
	}
}
