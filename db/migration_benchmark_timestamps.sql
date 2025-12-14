-- Migration: Add benchmark timestamps to jobs and job_shards tables
-- Run this if you have an existing database

-- Jobs table: add started_at and completed_at
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS started_at TIMESTAMP;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS completed_at TIMESTAMP;

-- Job_shards table: add timing and tracking columns
ALTER TABLE job_shards ADD COLUMN IF NOT EXISTS assigned_at TIMESTAMP;
ALTER TABLE job_shards ADD COLUMN IF NOT EXISTS started_at TIMESTAMP;
ALTER TABLE job_shards ADD COLUMN IF NOT EXISTS completed_at TIMESTAMP;
ALTER TABLE job_shards ADD COLUMN IF NOT EXISTS worker_name TEXT;
ALTER TABLE job_shards ADD COLUMN IF NOT EXISTS attempt_count INTEGER NOT NULL DEFAULT 0;
