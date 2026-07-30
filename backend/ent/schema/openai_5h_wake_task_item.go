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

// OpenAI5hWakeTaskItem stores one deduplicated upstream quota pool.
type OpenAI5hWakeTaskItem struct {
	ent.Schema
}

func (OpenAI5hWakeTaskItem) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "openai_5h_wake_task_items"}}
}

func (OpenAI5hWakeTaskItem) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("task_id"),
		field.String("identity_hash").MaxLen(64),
		field.JSON("member_account_ids", []int64{}).SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.JSON("attempted_account_ids", []int64{}).Default(func() []int64 { return []int64{} }).SchemaType(map[string]string{dialect.Postgres: "jsonb"}),
		field.Int64("successful_account_id").Optional().Nillable(),
		field.String("status").MaxLen(32).Default("pending"),
		field.Int("attempt_count").Default(0),
		field.String("error_code").Optional().Nillable().MaxLen(128),
		field.Time("reset_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("started_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("finished_at").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("created_at").Immutable().Default(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (OpenAI5hWakeTaskItem) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("task_id", "identity_hash").Unique(),
		index.Fields("task_id", "status"),
		index.Fields("status"),
	}
}
