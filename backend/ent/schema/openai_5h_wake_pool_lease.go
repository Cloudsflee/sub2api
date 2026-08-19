package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// OpenAI5hWakePoolLease serializes wake attempts for one upstream quota pool.
type OpenAI5hWakePoolLease struct {
	ent.Schema
}

func (OpenAI5hWakePoolLease) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "openai_5h_wake_pool_leases"}}
}

func (OpenAI5hWakePoolLease) Fields() []ent.Field {
	return []ent.Field{
		field.String("identity_hash").MaxLen(64).Unique(),
		field.Int64("task_id"),
		field.Int64("item_id").Unique(),
		field.String("lease_owner").MaxLen(128),
		field.Time("lease_expires_at").SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("heartbeat_at").Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (OpenAI5hWakePoolLease) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("task_id"),
		index.Fields("lease_expires_at"),
	}
}
