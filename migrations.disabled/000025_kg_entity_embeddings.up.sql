ALTER TABLE kg_entities ADD COLUMN IF NOT EXISTS embedding halfvec(3072);
CREATE INDEX IF NOT EXISTS idx_kg_entity_vec ON kg_entities USING hnsw(embedding halfvec_cosine_ops);
