package mongodb

import (
	"bytes"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestMongoIDFilterMarshalsIdenticalToScalarIDDoc(t *testing.T) {
	oid, err := primitive.ObjectIDFromHex("65f0a1b2c3d4e5f60718293a")
	if err != nil {
		t.Fatal(err)
	}
	for _, val := range []any{oid, int64(42), "raw-string-id", `{"$gt":""}`} {
		got, err := bson.Marshal(mongoIDFilter{ID: val})
		if err != nil {
			t.Fatalf("marshal mongoIDFilter(%T): %v", val, err)
		}
		want, err := bson.Marshal(bson.D{{Key: "_id", Value: val}})
		if err != nil {
			t.Fatalf("marshal bson.D(%T): %v", val, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("BSON mismatch for %v: got %x, want %x", val, got, want)
		}
	}
}
