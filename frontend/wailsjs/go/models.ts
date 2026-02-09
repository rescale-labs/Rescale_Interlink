export namespace wailsapp {
	
	export class AnalysisVersionDTO {
	    id: string;
	    version: string;
	    versionCode: string;
	    allowedCoreTypes: string[];
	
	    static createFrom(source: any = {}) {
	        return new AnalysisVersionDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.version = source["version"];
	        this.versionCode = source["versionCode"];
	        this.allowedCoreTypes = source["allowedCoreTypes"];
	    }
	}
	export class AnalysisCodeDTO {
	    code: string;
	    name: string;
	    description: string;
	    vendorName: string;
	    versions: AnalysisVersionDTO[];
	
	    static createFrom(source: any = {}) {
	        return new AnalysisCodeDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.vendorName = source["vendorName"];
	        this.versions = this.convertValues(source["versions"], AnalysisVersionDTO);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class AnalysisCodesResultDTO {
	    codes: AnalysisCodeDTO[];
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new AnalysisCodesResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.codes = this.convertValues(source["codes"], AnalysisCodeDTO);
	        this.error = source["error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class AppInfoDTO {
	    version: string;
	    buildTime: string;
	    fipsEnabled: boolean;
	    fipsStatus: string;
	
	    static createFrom(source: any = {}) {
	        return new AppInfoDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.version = source["version"];
	        this.buildTime = source["buildTime"];
	        this.fipsEnabled = source["fipsEnabled"];
	        this.fipsStatus = source["fipsStatus"];
	    }
	}
	export class AutoDownloadValidationDTO {
	    customFieldsEnabled: boolean;
	    hasAutoDownloadField: boolean;
	    autoDownloadFieldType: string;
	    autoDownloadFieldSection: string;
	    availableValues: string[];
	    hasAutoDownloadPathField: boolean;
	    warnings: string[];
	    errors: string[];
	
	    static createFrom(source: any = {}) {
	        return new AutoDownloadValidationDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.customFieldsEnabled = source["customFieldsEnabled"];
	        this.hasAutoDownloadField = source["hasAutoDownloadField"];
	        this.autoDownloadFieldType = source["autoDownloadFieldType"];
	        this.autoDownloadFieldSection = source["autoDownloadFieldSection"];
	        this.availableValues = source["availableValues"];
	        this.hasAutoDownloadPathField = source["hasAutoDownloadPathField"];
	        this.warnings = source["warnings"];
	        this.errors = source["errors"];
	    }
	}
	export class AutomationDTO {
	    id: string;
	    name: string;
	    description: string;
	    executeOn: string;
	    scriptName: string;
	
	    static createFrom(source: any = {}) {
	        return new AutomationDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.description = source["description"];
	        this.executeOn = source["executeOn"];
	        this.scriptName = source["scriptName"];
	    }
	}
	export class AutomationsResultDTO {
	    automations: AutomationDTO[];
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new AutomationsResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.automations = this.convertValues(source["automations"], AutomationDTO);
	        this.error = source["error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ConfigDTO {
	    apiBaseUrl: string;
	    tenantUrl: string;
	    apiKey: string;
	    proxyMode: string;
	    proxyHost: string;
	    proxyPort: number;
	    proxyUser: string;
	    proxyPassword: string;
	    noProxy: string;
	    proxyWarmup: boolean;
	    tarWorkers: number;
	    uploadWorkers: number;
	    jobWorkers: number;
	    excludePatterns: string;
	    includePatterns: string;
	    flattenTar: boolean;
	    tarCompression: string;
	    validationPattern: string;
	    runSubpath: string;
	    maxRetries: number;
	    detailedLogging: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ConfigDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.apiBaseUrl = source["apiBaseUrl"];
	        this.tenantUrl = source["tenantUrl"];
	        this.apiKey = source["apiKey"];
	        this.proxyMode = source["proxyMode"];
	        this.proxyHost = source["proxyHost"];
	        this.proxyPort = source["proxyPort"];
	        this.proxyUser = source["proxyUser"];
	        this.proxyPassword = source["proxyPassword"];
	        this.noProxy = source["noProxy"];
	        this.proxyWarmup = source["proxyWarmup"];
	        this.tarWorkers = source["tarWorkers"];
	        this.uploadWorkers = source["uploadWorkers"];
	        this.jobWorkers = source["jobWorkers"];
	        this.excludePatterns = source["excludePatterns"];
	        this.includePatterns = source["includePatterns"];
	        this.flattenTar = source["flattenTar"];
	        this.tarCompression = source["tarCompression"];
	        this.validationPattern = source["validationPattern"];
	        this.runSubpath = source["runSubpath"];
	        this.maxRetries = source["maxRetries"];
	        this.detailedLogging = source["detailedLogging"];
	    }
	}
	export class ConnectionResultDTO {
	    success: boolean;
	    email?: string;
	    fullName?: string;
	    workspaceId?: string;
	    workspaceName?: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.email = source["email"];
	        this.fullName = source["fullName"];
	        this.workspaceId = source["workspaceId"];
	        this.workspaceName = source["workspaceName"];
	        this.error = source["error"];
	    }
	}
	export class CoreTypeDTO {
	    code: string;
	    name: string;
	    displayOrder: number;
	    isActive: boolean;
	    cores: number[];
	
	    static createFrom(source: any = {}) {
	        return new CoreTypeDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.name = source["name"];
	        this.displayOrder = source["displayOrder"];
	        this.isActive = source["isActive"];
	        this.cores = source["cores"];
	    }
	}
	export class CoreTypesResultDTO {
	    coreTypes: CoreTypeDTO[];
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new CoreTypesResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.coreTypes = this.convertValues(source["coreTypes"], CoreTypeDTO);
	        this.error = source["error"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class DaemonConfigDTO {
	    enabled: boolean;
	    downloadFolder: string;
	    pollIntervalMinutes: number;
	    useJobNameDir: boolean;
	    maxConcurrent: number;
	    lookbackDays: number;
	    namePrefix: string;
	    nameContains: string;
	    exclude: string;
	    autoDownloadTag: string;
	    notificationsEnabled: boolean;
	    showDownloadComplete: boolean;
	    showDownloadFailed: boolean;
	    configPath: string;
	
	    static createFrom(source: any = {}) {
	        return new DaemonConfigDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.downloadFolder = source["downloadFolder"];
	        this.pollIntervalMinutes = source["pollIntervalMinutes"];
	        this.useJobNameDir = source["useJobNameDir"];
	        this.maxConcurrent = source["maxConcurrent"];
	        this.lookbackDays = source["lookbackDays"];
	        this.namePrefix = source["namePrefix"];
	        this.nameContains = source["nameContains"];
	        this.exclude = source["exclude"];
	        this.autoDownloadTag = source["autoDownloadTag"];
	        this.notificationsEnabled = source["notificationsEnabled"];
	        this.showDownloadComplete = source["showDownloadComplete"];
	        this.showDownloadFailed = source["showDownloadFailed"];
	        this.configPath = source["configPath"];
	    }
	}
	export class DaemonLogEntryDTO {
	    timestamp: string;
	    level: string;
	    stage: string;
	    message: string;
	    fields?: Record<string, any>;
	
	    static createFrom(source: any = {}) {
	        return new DaemonLogEntryDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.timestamp = source["timestamp"];
	        this.level = source["level"];
	        this.stage = source["stage"];
	        this.message = source["message"];
	        this.fields = source["fields"];
	    }
	}
	export class DaemonStatusDTO {
	    running: boolean;
	    pid: number;
	    ipcConnected: boolean;
	    state: string;
	    version: string;
	    uptime: string;
	    lastScan: string;
	    activeDownloads: number;
	    jobsDownloaded: number;
	    downloadFolder: string;
	    error?: string;
	    managedBy?: string;
	    serviceMode: boolean;
	    userConfigured: boolean;
	    userState: string;
	    userRegistered: boolean;
	
	    static createFrom(source: any = {}) {
	        return new DaemonStatusDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.pid = source["pid"];
	        this.ipcConnected = source["ipcConnected"];
	        this.state = source["state"];
	        this.version = source["version"];
	        this.uptime = source["uptime"];
	        this.lastScan = source["lastScan"];
	        this.activeDownloads = source["activeDownloads"];
	        this.jobsDownloaded = source["jobsDownloaded"];
	        this.downloadFolder = source["downloadFolder"];
	        this.error = source["error"];
	        this.managedBy = source["managedBy"];
	        this.serviceMode = source["serviceMode"];
	        this.userConfigured = source["userConfigured"];
	        this.userState = source["userState"];
	        this.userRegistered = source["userRegistered"];
	    }
	}
	export class DeleteResultDTO {
	    deleted: number;
	    failed: number;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new DeleteResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.deleted = source["deleted"];
	        this.failed = source["failed"];
	        this.error = source["error"];
	    }
	}
	export class ElevatedServiceResultDTO {
	    success: boolean;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new ElevatedServiceResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.error = source["error"];
	    }
	}
	export class FileItemDTO {
	    id: string;
	    name: string;
	    isFolder: boolean;
	    size: number;
	    modTime: string;
	    path?: string;
	    parentId?: string;
	
	    static createFrom(source: any = {}) {
	        return new FileItemDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.isFolder = source["isFolder"];
	        this.size = source["size"];
	        this.modTime = source["modTime"];
	        this.path = source["path"];
	        this.parentId = source["parentId"];
	    }
	}
	export class FileLoggingSettingsDTO {
	    enabled: boolean;
	    filePath: string;
	
	    static createFrom(source: any = {}) {
	        return new FileLoggingSettingsDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.enabled = source["enabled"];
	        this.filePath = source["filePath"];
	    }
	}
	export class FolderContentsDTO {
	    folderId: string;
	    folderPath: string;
	    items: FileItemDTO[];
	    hasMore: boolean;
	    nextCursor?: string;
	    isSlowPath?: boolean;
	    warning?: string;
	
	    static createFrom(source: any = {}) {
	        return new FolderContentsDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.folderId = source["folderId"];
	        this.folderPath = source["folderPath"];
	        this.items = this.convertValues(source["items"], FileItemDTO);
	        this.hasMore = source["hasMore"];
	        this.nextCursor = source["nextCursor"];
	        this.isSlowPath = source["isSlowPath"];
	        this.warning = source["warning"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class FolderDownloadResultDTO {
	    foldersCreated: number;
	    filesDownloaded: number;
	    filesSkipped: number;
	    filesFailed: number;
	    totalBytes: number;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new FolderDownloadResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.foldersCreated = source["foldersCreated"];
	        this.filesDownloaded = source["filesDownloaded"];
	        this.filesSkipped = source["filesSkipped"];
	        this.filesFailed = source["filesFailed"];
	        this.totalBytes = source["totalBytes"];
	        this.error = source["error"];
	    }
	}
	export class FolderExistsCheckDTO {
	    exists: boolean;
	    folderId?: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new FolderExistsCheckDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.exists = source["exists"];
	        this.folderId = source["folderId"];
	        this.error = source["error"];
	    }
	}
	export class FolderUploadResultDTO {
	    foldersCreated: number;
	    filesQueued: number;
	    totalBytes: number;
	    mergedInto?: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new FolderUploadResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.foldersCreated = source["foldersCreated"];
	        this.filesQueued = source["filesQueued"];
	        this.totalBytes = source["totalBytes"];
	        this.mergedInto = source["mergedInto"];
	        this.error = source["error"];
	    }
	}
	export class JobRowDTO {
	    index: number;
	    directory: string;
	    jobName: string;
	    tarStatus: string;
	    uploadStatus: string;
	    uploadProgress: number;
	    createStatus: string;
	    submitStatus: string;
	    status: string;
	    jobId: string;
	    progress: number;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new JobRowDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.index = source["index"];
	        this.directory = source["directory"];
	        this.jobName = source["jobName"];
	        this.tarStatus = source["tarStatus"];
	        this.uploadStatus = source["uploadStatus"];
	        this.uploadProgress = source["uploadProgress"];
	        this.createStatus = source["createStatus"];
	        this.submitStatus = source["submitStatus"];
	        this.status = source["status"];
	        this.jobId = source["jobId"];
	        this.progress = source["progress"];
	        this.error = source["error"];
	    }
	}
	export class JobSpecDTO {
	    directory: string;
	    jobName: string;
	    analysisCode: string;
	    analysisVersion: string;
	    command: string;
	    coreType: string;
	    coresPerSlot: number;
	    walltimeHours: number;
	    slots: number;
	    licenseSettings: string;
	    extraInputFileIds: string;
	    noDecompress: boolean;
	    submitMode: string;
	    isLowPriority: boolean;
	    onDemandLicenseSeller: string;
	    tags: string[];
	    projectId: string;
	    automations: string[];
	    inputFiles?: string[];
	    tarSubpath?: string;
	
	    static createFrom(source: any = {}) {
	        return new JobSpecDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.directory = source["directory"];
	        this.jobName = source["jobName"];
	        this.analysisCode = source["analysisCode"];
	        this.analysisVersion = source["analysisVersion"];
	        this.command = source["command"];
	        this.coreType = source["coreType"];
	        this.coresPerSlot = source["coresPerSlot"];
	        this.walltimeHours = source["walltimeHours"];
	        this.slots = source["slots"];
	        this.licenseSettings = source["licenseSettings"];
	        this.extraInputFileIds = source["extraInputFileIds"];
	        this.noDecompress = source["noDecompress"];
	        this.submitMode = source["submitMode"];
	        this.isLowPriority = source["isLowPriority"];
	        this.onDemandLicenseSeller = source["onDemandLicenseSeller"];
	        this.tags = source["tags"];
	        this.projectId = source["projectId"];
	        this.automations = source["automations"];
	        this.inputFiles = source["inputFiles"];
	        this.tarSubpath = source["tarSubpath"];
	    }
	}
	export class JobsStatsDTO {
	    total: number;
	    completed: number;
	    inProgress: number;
	    pending: number;
	    failed: number;
	
	    static createFrom(source: any = {}) {
	        return new JobsStatsDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.total = source["total"];
	        this.completed = source["completed"];
	        this.inProgress = source["inProgress"];
	        this.pending = source["pending"];
	        this.failed = source["failed"];
	    }
	}
	export class LocalFileInfoDTO {
	    path: string;
	    name: string;
	    isDir: boolean;
	    size: number;
	    fileCount: number;
	    modTime: string;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new LocalFileInfoDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.path = source["path"];
	        this.name = source["name"];
	        this.isDir = source["isDir"];
	        this.size = source["size"];
	        this.fileCount = source["fileCount"];
	        this.modTime = source["modTime"];
	        this.error = source["error"];
	    }
	}
	export class RunStatusDTO {
	    state: string;
	    totalJobs: number;
	    successJobs: number;
	    failedJobs: number;
	    durationMs: number;
	    error?: string;
	
	    static createFrom(source: any = {}) {
	        return new RunStatusDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.state = source["state"];
	        this.totalJobs = source["totalJobs"];
	        this.successJobs = source["successJobs"];
	        this.failedJobs = source["failedJobs"];
	        this.durationMs = source["durationMs"];
	        this.error = source["error"];
	    }
	}
	export class SecondaryPatternDTO {
	    pattern: string;
	    required: boolean;
	
	    static createFrom(source: any = {}) {
	        return new SecondaryPatternDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.pattern = source["pattern"];
	        this.required = source["required"];
	    }
	}
	export class ScanOptionsDTO {
	    rootDir: string;
	    pattern: string;
	    validationPattern: string;
	    runSubpath: string;
	    recursive: boolean;
	    includeHidden: boolean;
	    scanMode: string;
	    primaryPattern: string;
	    secondaryPatterns: SecondaryPatternDTO[];
	    tarSubpath?: string;
	
	    static createFrom(source: any = {}) {
	        return new ScanOptionsDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.rootDir = source["rootDir"];
	        this.pattern = source["pattern"];
	        this.validationPattern = source["validationPattern"];
	        this.runSubpath = source["runSubpath"];
	        this.recursive = source["recursive"];
	        this.includeHidden = source["includeHidden"];
	        this.scanMode = source["scanMode"];
	        this.primaryPattern = source["primaryPattern"];
	        this.secondaryPatterns = this.convertValues(source["secondaryPatterns"], SecondaryPatternDTO);
	        this.tarSubpath = source["tarSubpath"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class ScanResultDTO {
	    jobs: JobSpecDTO[];
	    totalCount: number;
	    matchCount: number;
	    invalidDirs: string[];
	    error?: string;
	    skippedFiles?: string[];
	    warnings?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ScanResultDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.jobs = this.convertValues(source["jobs"], JobSpecDTO);
	        this.totalCount = source["totalCount"];
	        this.matchCount = source["matchCount"];
	        this.invalidDirs = source["invalidDirs"];
	        this.error = source["error"];
	        this.skippedFiles = source["skippedFiles"];
	        this.warnings = source["warnings"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class ServiceStatusDTO {
	    installed: boolean;
	    running: boolean;
	    status: string;
	    scmBlocked: boolean;
	    scmError: string;
	
	    static createFrom(source: any = {}) {
	        return new ServiceStatusDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.installed = source["installed"];
	        this.running = source["running"];
	        this.status = source["status"];
	        this.scmBlocked = source["scmBlocked"];
	        this.scmError = source["scmError"];
	    }
	}
	export class SingleJobInputDTO {
	    job: JobSpecDTO;
	    inputMode: string;
	    directory?: string;
	    localFiles?: string[];
	    remoteFileIds?: string[];
	
	    static createFrom(source: any = {}) {
	        return new SingleJobInputDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.job = this.convertValues(source["job"], JobSpecDTO);
	        this.inputMode = source["inputMode"];
	        this.directory = source["directory"];
	        this.localFiles = source["localFiles"];
	        this.remoteFileIds = source["remoteFileIds"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TemplateInfoDTO {
	    name: string;
	    path: string;
	    description: string;
	    software: string;
	    hardware: string;
	    modTime: string;
	    job?: JobSpecDTO;
	
	    static createFrom(source: any = {}) {
	        return new TemplateInfoDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.name = source["name"];
	        this.path = source["path"];
	        this.description = source["description"];
	        this.software = source["software"];
	        this.hardware = source["hardware"];
	        this.modTime = source["modTime"];
	        this.job = this.convertValues(source["job"], JobSpecDTO);
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class TransferRequestDTO {
	    type: string;
	    source: string;
	    dest: string;
	    name: string;
	    size: number;
	
	    static createFrom(source: any = {}) {
	        return new TransferRequestDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.source = source["source"];
	        this.dest = source["dest"];
	        this.name = source["name"];
	        this.size = source["size"];
	    }
	}
	export class TransferStatsDTO {
	    queued: number;
	    initializing: number;
	    active: number;
	    paused: number;
	    completed: number;
	    failed: number;
	    cancelled: number;
	    total: number;
	
	    static createFrom(source: any = {}) {
	        return new TransferStatsDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.queued = source["queued"];
	        this.initializing = source["initializing"];
	        this.active = source["active"];
	        this.paused = source["paused"];
	        this.completed = source["completed"];
	        this.failed = source["failed"];
	        this.cancelled = source["cancelled"];
	        this.total = source["total"];
	    }
	}
	export class TransferTaskDTO {
	    id: string;
	    type: string;
	    state: string;
	    name: string;
	    source: string;
	    dest: string;
	    size: number;
	    progress: number;
	    speed: number;
	    error?: string;
	    createdAt: string;
	    startedAt?: string;
	    completedAt?: string;
	
	    static createFrom(source: any = {}) {
	        return new TransferTaskDTO(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.type = source["type"];
	        this.state = source["state"];
	        this.name = source["name"];
	        this.source = source["source"];
	        this.dest = source["dest"];
	        this.size = source["size"];
	        this.progress = source["progress"];
	        this.speed = source["speed"];
	        this.error = source["error"];
	        this.createdAt = source["createdAt"];
	        this.startedAt = source["startedAt"];
	        this.completedAt = source["completedAt"];
	    }
	}

}

