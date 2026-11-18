export namespace config {
	
	export class Credentials {
	    awsRegion: string;
	    apiKey: string;
	    apiSecret: string;
	
	    static createFrom(source: any = {}) {
	        return new Credentials(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.awsRegion = source["awsRegion"];
	        this.apiKey = source["apiKey"];
	        this.apiSecret = source["apiSecret"];
	    }
	}

}

export namespace latency {
	
	export class Point {
	    table: string;
	    snapshotId: number;
	    records: number;
	    // Go type: time
	    recordAt: any;
	    // Go type: time
	    newestAt: any;
	    // Go type: time
	    committedAt: any;
	    latencyMs: number;
	    minLatencyMs: number;
	    // Go type: time
	    observedAt: any;
	
	    static createFrom(source: any = {}) {
	        return new Point(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.table = source["table"];
	        this.snapshotId = source["snapshotId"];
	        this.records = source["records"];
	        this.recordAt = this.convertValues(source["recordAt"], null);
	        this.newestAt = this.convertValues(source["newestAt"], null);
	        this.committedAt = this.convertValues(source["committedAt"], null);
	        this.latencyMs = source["latencyMs"];
	        this.minLatencyMs = source["minLatencyMs"];
	        this.observedAt = this.convertValues(source["observedAt"], null);
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
	export class TableError {
	    table: string;
	    error: string;
	
	    static createFrom(source: any = {}) {
	        return new TableError(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.table = source["table"];
	        this.error = source["error"];
	    }
	}

}

export namespace logging {
	
	export class Entry {
	    // Go type: time
	    time: any;
	    level: string;
	    message: string;
	    attrs: string;
	
	    static createFrom(source: any = {}) {
	        return new Entry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = this.convertValues(source["time"], null);
	        this.level = source["level"];
	        this.message = source["message"];
	        this.attrs = source["attrs"];
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

}

export namespace main {
	
	export class Defaults {
	    awsRegion: string;
	    workspace: string;
	
	    static createFrom(source: any = {}) {
	        return new Defaults(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.awsRegion = source["awsRegion"];
	        this.workspace = source["workspace"];
	    }
	}
	export class State {
	    hasCredentials: boolean;
	    awsRegion: string;
	    busy: string;
	    lastError: string;
	    steps: pipeline.StepState[];
	    sim: sim.Stats;
	    logs: logging.Entry[];
	    latency: latency.Point[];
	    tableErrors: latency.TableError[];
	    limits: Record<string, number>;
	
	    static createFrom(source: any = {}) {
	        return new State(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.hasCredentials = source["hasCredentials"];
	        this.awsRegion = source["awsRegion"];
	        this.busy = source["busy"];
	        this.lastError = source["lastError"];
	        this.steps = this.convertValues(source["steps"], pipeline.StepState);
	        this.sim = this.convertValues(source["sim"], sim.Stats);
	        this.logs = this.convertValues(source["logs"], logging.Entry);
	        this.latency = this.convertValues(source["latency"], latency.Point);
	        this.tableErrors = this.convertValues(source["tableErrors"], latency.TableError);
	        this.limits = source["limits"];
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

}

export namespace pipeline {
	
	export class StepState {
	    id: string;
	    title: string;
	    status: string;
	    detail?: string;
	    // Go type: time
	    startedAt: any;
	    // Go type: time
	    finishedAt: any;
	
	    static createFrom(source: any = {}) {
	        return new StepState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.title = source["title"];
	        this.status = source["status"];
	        this.detail = source["detail"];
	        this.startedAt = this.convertValues(source["startedAt"], null);
	        this.finishedAt = this.convertValues(source["finishedAt"], null);
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

}

export namespace sim {
	
	export class Stats {
	    running: boolean;
	    rate: number;
	    events: number;
	    byKind: Record<string, number>;
	    conflicts: number;
	    errors: number;
	    lastError?: string;
	    // Go type: time
	    simulatedAt: any;
	    patients: number;
	    scheduled: number;
	    inClinic: number;
	
	    static createFrom(source: any = {}) {
	        return new Stats(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.running = source["running"];
	        this.rate = source["rate"];
	        this.events = source["events"];
	        this.byKind = source["byKind"];
	        this.conflicts = source["conflicts"];
	        this.errors = source["errors"];
	        this.lastError = source["lastError"];
	        this.simulatedAt = this.convertValues(source["simulatedAt"], null);
	        this.patients = source["patients"];
	        this.scheduled = source["scheduled"];
	        this.inClinic = source["inClinic"];
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

}

