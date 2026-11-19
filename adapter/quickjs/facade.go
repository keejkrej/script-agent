package quickjs

// FacadeSource is the trusted prelude that defines a frozen global `fs`
// namespace on top of the public codemode tools installed by Install.
//
// Set it as CodemodeSandboxOptions.TrustedPreludeSource. The runtime evaluates
// it in every fresh VM after the codemode prelude installs tools and before
// the user's script. It never receives the private host bridge.
//
// The namespace is intentionally small and Node-shaped:
//
//   - fs.readFile(path[, options]) resolves to a Uint8Array, or to a string
//     when options is "utf8"/"utf-8" or { encoding: "utf8"|"utf-8" }.
//   - fs.writeFile(path, string|Uint8Array) resolves to undefined. A
//     Uint8Array is written exactly as its view (byteOffset/byteLength), not
//     as the whole backing buffer.
//   - fs.readdir([path]) resolves to a sorted array of entry names.
//   - fs.mkdir(path[, { recursive }]) resolves to a boolean: true when a
//     directory was created, false when recursive:true found an existing
//     directory. This is a documented deviation from Node, which returns the
//     first created path.
//   - fs.rm(path) removes a file, symlink, or empty directory and resolves to
//     undefined. There is no recursive removal; any non-empty options object
//     is rejected.
//   - fs.exists(path) resolves to a boolean. This is a custom helper, not a
//     Node API.
//
// Every function is async and returns a real Promise, including for argument
// errors. Backend capability codes are mapped to Node-style `.code` values
// (ENOENT, EACCES, EINVAL, EISDIR, ENOTDIR, EEXIST, ENOTEMPTY, EFBIG, EIO) and
// the original capability code is kept on `.capabilityCode`. The relative
// path is kept on `.path`. Local validation uses ERR_INVALID_ARG_TYPE,
// ERR_UNKNOWN_ENCODING, and ERR_UNSUPPORTED_OPERATION.
//
// The facade captures the intrinsics it needs before untrusted code runs, so
// later monkey patching of Uint8Array, atob, btoa, Object, or Array does not
// change its behavior. It uses only the public `tools` object; the private
// host bridge is never referenced.
const FacadeSource = `(function () {
	"use strict";
	const Uint8ArrayCtor = Uint8Array;
	const Uint8ArrayPrototype = Uint8Array.prototype;
	const atobFn = atob;
	const btoaFn = btoa;
	const ObjectCtor = Object;
	const ArrayCtor = Array;
	const StringCtor = String;
	const ErrorCtor = Error;
	const TypeErrorCtor = TypeError;
	const defineProperty = Object.defineProperty;
	const getOwnPropertyDescriptor = Object.getOwnPropertyDescriptor;
	const objectKeys = Object.keys;
	const freeze = Object.freeze;
	const isArray = Array.isArray;
	const isPrototypeOf = Object.prototype.isPrototypeOf;
	const fromCharCode = String.fromCharCode;
	const mathMin = Math.min;
	const toolsRef = tools;

	const NODE_CODES = {
		not_found: "ENOENT",
		permission_denied: "EACCES",
		invalid_path: "EINVAL",
		invalid_argument: "EINVAL",
		tool_argument: "EINVAL",
		too_large: "EFBIG",
		is_directory: "EISDIR",
		not_directory: "ENOTDIR",
		exists: "EEXIST",
		not_empty: "ENOTEMPTY",
		io_error: "EIO",
		invalid_utf8: "EINVAL",
		invalid_base64: "EINVAL",
	};

	function codedError(code, message) {
		const error = new ErrorCtor(message);
		error.code = code;
		return error;
	}

	function invalidArg(name, expected, actual) {
		return codedError(
			"ERR_INVALID_ARG_TYPE",
			"The \"" + name + "\" argument must be " + expected + ". Received " + actual,
		);
	}

	function describe(value) {
		if (value === null) return "null";
		return typeof value;
	}

	function mapBackendError(error, path) {
		const raw = error && typeof error.code === "string" ? error.code : "";
		const nodeCode = NODE_CODES[raw] || "EIO";
		const message = error && typeof error.message === "string" ? error.message : StringCtor(error);
		const mapped = new ErrorCtor(message);
		mapped.code = nodeCode;
		mapped.capabilityCode = raw;
		if (path !== undefined) mapped.path = path;
		return mapped;
	}

	function checkPath(path) {
		if (typeof path !== "string") {
			throw invalidArg("path", "of type string", describe(path));
		}
	}

	function normalizeEncoding(options) {
		if (options === undefined || options === null) return "binary";
		if (typeof options === "string") {
			const encoding = options.toLowerCase();
			if (encoding === "utf8" || encoding === "utf-8") return "utf8";
			throw codedError("ERR_UNKNOWN_ENCODING", "Unknown encoding: " + options);
		}
		if (typeof options !== "object" || isArray(options)) {
			throw invalidArg("options", "of type object or string", describe(options));
		}
		const keys = objectKeys(options);
		for (let i = 0; i < keys.length; i++) {
			if (keys[i] !== "encoding") {
				throw codedError(
					"ERR_INVALID_ARG_TYPE",
					"The \"options\" argument contains an unsupported property \"" + keys[i] + "\"",
				);
			}
		}
		const encoding = options.encoding;
		if (encoding === undefined || encoding === null) return "binary";
		if (typeof encoding !== "string") {
			throw invalidArg("options.encoding", "of type string", describe(encoding));
		}
		const normalized = encoding.toLowerCase();
		if (normalized === "utf8" || normalized === "utf-8") return "utf8";
		throw codedError("ERR_UNKNOWN_ENCODING", "Unknown encoding: " + encoding);
	}

	function base64ToBytes(encoded) {
		const binary = atobFn(encoded);
		const length = binary.length;
		const bytes = new Uint8ArrayCtor(length);
		for (let i = 0; i < length; i++) bytes[i] = binary.charCodeAt(i);
		return bytes;
	}

	function bytesToBase64(bytes) {
		const chunkSize = 0x8000;
		let binary = "";
		for (let offset = 0; offset < bytes.length; offset += chunkSize) {
			const end = mathMin(offset + chunkSize, bytes.length);
			let chunk = "";
			for (let i = offset; i < end; i++) chunk += fromCharCode(bytes[i]);
			binary += chunk;
		}
		return btoaFn(binary);
	}

	function isUint8Array(value) {
		return value !== null && typeof value === "object" && isPrototypeOf.call(Uint8ArrayPrototype, value);
	}

	function requireText(result) {
		if (result === null || typeof result !== "object" || typeof result.text !== "string") {
			throw new TypeErrorCtor("fs.readFile: backend returned an invalid text result");
		}
		return result.text;
	}

	function requireBase64(result) {
		if (result === null || typeof result !== "object" || typeof result.base64 !== "string") {
			throw new TypeErrorCtor("fs.readFile: backend returned an invalid binary result");
		}
		return result.base64;
	}

	function requireEntries(result) {
		if (result === null || typeof result !== "object" || !isArray(result.entries)) {
			throw new TypeErrorCtor("fs.readdir: backend returned an invalid entries result");
		}
		return result.entries;
	}

	function requireCreated(result) {
		if (result === null || typeof result !== "object" || typeof result.created !== "boolean") {
			throw new TypeErrorCtor("fs.mkdir: backend returned an invalid created result");
		}
		return result.created;
	}

	function requireExists(result) {
		if (result === null || typeof result !== "object" || typeof result.exists !== "boolean") {
			throw new TypeErrorCtor("fs.exists: backend returned an invalid exists result");
		}
		return result.exists;
	}

	async function readFile(path, options) {
		checkPath(path);
		const mode = normalizeEncoding(options);
		if (mode === "utf8") {
			let result;
			try {
				result = await toolsRef.fs_read_text({ path: path, encoding: "utf-8" });
			} catch (error) {
				throw mapBackendError(error, path);
			}
			return requireText(result);
		}
		let result;
		try {
			result = await toolsRef.fs_read_bytes({ path: path });
		} catch (error) {
			throw mapBackendError(error, path);
		}
		return base64ToBytes(requireBase64(result));
	}

	async function writeFile(path, data, options) {
		checkPath(path);
		if (options !== undefined && options !== null) {
			throw codedError("ERR_UNSUPPORTED_OPERATION", "fs.writeFile options are not supported");
		}
		if (typeof data === "string") {
			try {
				await toolsRef.fs_write_text({ path: path, text: data });
			} catch (error) {
				throw mapBackendError(error, path);
			}
			return undefined;
		}
		if (isUint8Array(data)) {
			const encoded = bytesToBase64(data);
			try {
				await toolsRef.fs_write_bytes({ path: path, base64: encoded });
			} catch (error) {
				throw mapBackendError(error, path);
			}
			return undefined;
		}
		throw invalidArg("data", "of type string or an instance of Uint8Array", describe(data));
	}

	async function readdir(path, options) {
		if (path === undefined) path = ".";
		checkPath(path);
		if (options !== undefined && options !== null) {
			throw codedError("ERR_UNSUPPORTED_OPERATION", "fs.readdir options are not supported");
		}
		let result;
		try {
			result = await toolsRef.fs_listdir({ path: path });
		} catch (error) {
			throw mapBackendError(error, path);
		}
		const entries = requireEntries(result);
		const names = new ArrayCtor(entries.length);
		for (let i = 0; i < entries.length; i++) {
			const entry = entries[i];
			if (entry === null || typeof entry !== "object" || typeof entry.name !== "string") {
				throw new TypeErrorCtor("fs.readdir: backend returned an invalid entry");
			}
			names[i] = entry.name;
		}
		return names;
	}

	async function mkdir(path, options) {
		checkPath(path);
		let recursive = false;
		if (options !== undefined && options !== null) {
			if (typeof options !== "object" || isArray(options)) {
				throw invalidArg("options", "of type object", describe(options));
			}
			const keys = objectKeys(options);
			for (let i = 0; i < keys.length; i++) {
				if (keys[i] !== "recursive") {
					throw codedError(
						"ERR_INVALID_ARG_TYPE",
						"The \"options\" argument contains an unsupported property \"" + keys[i] + "\"",
					);
				}
			}
			if (options.recursive !== undefined) {
				if (typeof options.recursive !== "boolean") {
					throw invalidArg("options.recursive", "of type boolean", describe(options.recursive));
				}
				recursive = options.recursive;
			}
		}
		let result;
		try {
			result = await toolsRef.fs_mkdir({ path: path, parents: recursive });
		} catch (error) {
			throw mapBackendError(error, path);
		}
		return requireCreated(result);
	}

	async function rm(path, options) {
		checkPath(path);
		if (options !== undefined && options !== null) {
			if (typeof options !== "object" || isArray(options)) {
				throw invalidArg("options", "of type object", describe(options));
			}
			if (objectKeys(options).length > 0) {
				throw codedError(
					"ERR_UNSUPPORTED_OPERATION",
					"fs.rm options are not supported; recursive removal is not available",
				);
			}
		}
		try {
			await toolsRef.fs_remove({ path: path });
		} catch (error) {
			throw mapBackendError(error, path);
		}
		return undefined;
	}

	async function exists(path) {
		checkPath(path);
		let result;
		try {
			result = await toolsRef.fs_exists({ path: path });
		} catch (error) {
			throw mapBackendError(error, path);
		}
		return requireExists(result);
	}

	freeze(readFile);
	freeze(writeFile);
	freeze(readdir);
	freeze(mkdir);
	freeze(rm);
	freeze(exists);

	const fs = {
		readFile: readFile,
		writeFile: writeFile,
		readdir: readdir,
		mkdir: mkdir,
		rm: rm,
		exists: exists,
	};
	freeze(fs);

	if (getOwnPropertyDescriptor(globalThis, "fs") !== undefined) {
		throw new ErrorCtor("quickjsbridge: global fs is already defined");
	}
	defineProperty(globalThis, "fs", {
		value: fs,
		writable: false,
		enumerable: true,
		configurable: false,
	});
})();`
