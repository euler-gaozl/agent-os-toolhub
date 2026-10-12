package toolhub

// Projection is static SQL, evaluated before JSON crosses the DB boundary.
// It excludes tool schemas, full prompts, bindings and provider configuration.
const catalogProjection = `jsonb_build_object(
 'name',left(spec->>'name',200),
 'prompt',left(spec->>'prompt',240),
 'content',left(spec->>'content',240),
 'description',left(spec->>'description',240),
 'engine',left(spec->>'engine',40),
 'model',jsonb_build_object('model',left(spec#>>'{model,model}',100)),
 'publication',jsonb_build_object('enabled',spec#>>'{publication,enabled}'='true'),
 'skill_count',jsonb_array_length(coalesce(nullif(spec->'skills','null'::jsonb),'[]'::jsonb)),
 'connector_count',jsonb_array_length(coalesce(nullif(spec->'connectors','null'::jsonb),'[]'::jsonb)),
 'knowledge_count',jsonb_array_length(coalesce(nullif(spec->'knowledge','null'::jsonb),'[]'::jsonb)))`
