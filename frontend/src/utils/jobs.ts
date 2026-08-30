// Helpers over the job domain types in types/jobs.ts, shared by the stores and
// by the tabs that read job specs straight off the Go boundary.

import type { JobSpec, JobRow } from '../types/jobs'

// normalizeJobSpec makes a spec that arrived from Go safe to read directly.
// A nil Go slice marshals to null, so tags and automations arrive as null on a
// spec that has none while JobSpec declares them present — the first `.length`
// read would throw. orgCode is normalized alongside them so a spec restored
// from an older saved file cannot leave the field undefined.
//
// Every other field passes through, inputFiles and tarSubpath included; that
// pass-through is what keeps a load/save round-trip lossless.
export function normalizeJobSpec(spec: JobSpec): JobSpec {
  return {
    ...spec,
    tags: spec.tags || [],
    orgCode: spec.orgCode || '',
    automations: spec.automations || [],
  }
}

// makePendingJobRow builds the table row for a job that has not started yet.
// Every stage reads 'pending' so the table shows a complete pipeline from the
// moment the job list exists, before any event has arrived.
export function makePendingJobRow(
  row: Pick<JobRow, 'index' | 'directory' | 'jobName'>,
): JobRow {
  return {
    ...row,
    tarStatus: 'pending',
    uploadStatus: 'pending',
    uploadProgress: 0,
    createStatus: 'pending',
    submitStatus: 'pending',
    status: 'pending',
    jobId: '',
    progress: 0,
    error: '',
  }
}
