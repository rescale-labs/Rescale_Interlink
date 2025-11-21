# Simple Job Submission Example

This directory contains a simple SGE-style shell script (`simple_job.sh`) that can be used with both `rescale-cli` and `rescale-int` to submit a test job to Rescale.

## File Contents

- `simple_job.sh` - SGE-style shell script with embedded job parameters
- `simple_jobs.csv` - Job configuration file for PUR pipeline mode

## Using with rescale-int

### Method 1: Direct Job Submission

```bash
# 1. Upload the script
rescale-int files upload simple_job.sh

# 2. Submit job using the uploaded file ID
rescale-int jobs submit \
  --name "Simple Test Job" \
  --code user_included \
  --version 1.0 \
  --command "./simple_job.sh" \
  --coretype emerald \
  --cores 1 \
  --walltime 1.0 \
  --input-file <file-id-from-upload>
```

### Method 2: Using Job Builder

```bash
# Interactive job builder
rescale-int jobs submit

# Follow prompts:
# - Job name: Simple Test Job
# - Analysis code: user_included
# - Version: 1.0
# - Command: ./simple_job.sh
# - Core type: emerald
# - Cores per slot: 1
# - Walltime: 1.0
# - Slots: 1
# - Input files: Upload simple_job.sh
```

### Method 3: Using PUR Pipeline (for multiple jobs)

```bash
# Create a jobs CSV file (simple_jobs.csv already exists)
# Make sure Run_1 directory exists with simple_job.sh inside

mkdir -p Run_1
cp simple_job.sh Run_1/run.sh

# Run the pipeline
rescale-int pur run \
  --jobs-csv simple_jobs.csv \
  --state state.csv
```

## Using with rescale-cli

```bash
# 1. Upload the script
rescale-cli files upload simple_job.sh

# 2. Get the file ID from output (e.g., XxYyZz)

# 3. Create job JSON
cat > job.json << 'EOF'
{
  "name": "Simple Test Job",
  "jobanalyses": [{
    "analysis": {
      "code": "user_included",
      "version": "1.0"
    },
    "command": "./simple_job.sh",
    "hardware": {
      "coresPerSlot": 1,
      "slots": 1,
      "coreType": {
        "code": "emerald"
      },
      "walltime": 1
    },
    "inputFiles": [{
      "id": "XxYyZz"
    }]
  }]
}
EOF

# 4. Submit the job
rescale-cli jobs submit --input job.json
```

## What the Script Does

The `simple_job.sh` script:

1. Prints job information (name, ID, hostname, etc.)
2. Lists files in the working directory
3. Performs a simple computation (sum of 1 to 1,000,000)
4. Creates a results file (`results.txt`)
5. Displays the results
6. Exits successfully

## Expected Output Files

After job completion, you should see:
- `results.txt` - Job results summary
- `job_output.log` - Standard output/error (if SGE directives are honored)
- `process_output.log` - Rescale process output

## Downloading Results

### Using rescale-int

```bash
# List your jobs to find the job ID
rescale-int jobs list

# Download all job output files
rescale-int jobs download --id <job-id> --outdir ./results
```

### Using rescale-cli

```bash
# List jobs
rescale-cli jobs list

# Download output files
rescale-cli files download <output-file-id> --output-dir ./results
```

## Core Type Options

The script is configured to use the `emerald` core type (1 core). You can modify this to use other core types:

- `emerald` - Basic compute (1 core)
- `onyx` - Standard compute (8 cores)
- `sapphire` - High-performance compute (16 cores)
- `amber` - GPU-enabled compute

To see all available core types:
```bash
rescale-int hardware list
```

## Modifying the Script

To customize the job for your needs:

1. Edit `simple_job.sh` to change the computation
2. Update the SGE directives (`#$`) for different resources
3. Modify the environment variables for different Rescale settings
4. Add your own data processing logic

## Troubleshooting

If the job fails:

1. Check job status: `rescale-int jobs get --id <job-id>`
2. Download logs: `rescale-int jobs download --id <job-id>`
3. Review `process_output.log` for errors
4. Ensure script has execute permissions (should be set automatically on Rescale)

## Notes

- The script uses `user_included` analysis code, which allows you to bring your own software
- Walltime is set to 1 hour; the job should complete in seconds
- The script is self-contained and doesn't require external dependencies
- SGE directives are for reference; Rescale uses the API parameters for resource allocation
