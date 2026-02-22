function dispatch(method, paramsJSON, stateJSON) {
	const params = parseJSON(paramsJSON, {});
	const state = normalizeState(parseJSON(stateJSON, {}));

	let result;
	switch (method) {
		case "command.handle":
			result = handleCommand(params);
			break;
		default:
			throw new Error("unsupported method: " + method);
	}

	return JSON.stringify({
		result,
		state,
	});
}

function parseJSON(input, fallback) {
	try {
		return JSON.parse(input);
	} catch (_error) {
		return fallback;
	}
}

function normalizeState(input) {
	if (typeof input === "object" && input !== null) {
		return input;
	}
	return {};
}

function handleCommand(params) {
	const text = String(params.text || "").trim();
	if (!text.startsWith("/")) {
		return { handled: false, response: "" };
	}

	const parts = text.split(/\s+/).filter(Boolean);
	if (parts.length === 0 || parts[0].toLowerCase() !== "/cstunnel") {
		return { handled: false, response: "" };
	}

	if (parts.length === 1) {
		return actionResponse("pair");
	}

	const sub = String(parts[1] || "").toLowerCase();
	switch (sub) {
		case "help":
			return { handled: true, response: helpText() };
		case "status":
			return actionResponse("status");
		case "pair":
			return actionResponse("pair");
		case "stop":
		case "off":
			return actionResponse("tunnel_off");
		default:
			return {
				handled: true,
				response: "usage: /cstunnel | /cstunnel status | /cstunnel stop | /cstunnel help",
			};
	}
}

function actionResponse(actionType) {
	const action = {
		type: actionType,
	};
	return {
		handled: true,
		response: "",
		action: action,
	};
}

function helpText() {
	return [
		"cstunnel commands:",
		"/cstunnel         run pairing flow",
		"/cstunnel status  show current state",
		"/cstunnel stop    stop cloudflared tunnel",
		"/cstunnel help",
	].join("\n");
}
