export namespace main {
	
	export class PrivateKeyStatus {
	    found: boolean;
	    path: string;
	    message: string;
	
	    static createFrom(source: any = {}) {
	        return new PrivateKeyStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.found = source["found"];
	        this.path = source["path"];
	        this.message = source["message"];
	    }
	}

}

