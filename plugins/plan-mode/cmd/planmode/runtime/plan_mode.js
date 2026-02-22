const PLAN_PROMPT = `[PLAN MODE ACTIVE]
You are in plan mode, a read-only exploration mode for safe code analysis.

Restrictions:
- Do not edit files or execute destructive actions.
- Gather context, inspect code, and propose a concrete implementation path.
- Produce a detailed numbered plan under a "Plan:" header.
- Do not execute the plan yet unless the user explicitly asks.`;

const EXECUTION_PROMPT = `[EXECUTING PLAN]
Execute the approved plan in order.
When a step is completed, include a [DONE:n] marker where n is the step number.`;

function dispatch(method, paramsJSON, stateJSON) {
	const params = parseJSON(paramsJSON, {});
	const state = normalizeState(parseJSON(stateJSON, {}));
	let result;

	switch (method) {
		case "command.handle":
			result = handleCommand(params, state);
			break;
		case "hook.before_turn":
			result = handleBeforeTurn(params, state);
			break;
		case "hook.after_turn":
			result = handleAfterTurn(params, state);
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
	const src = typeof input === "object" && input !== null ? input : {};
	const todos = Array.isArray(src.todos) ? src.todos : [];
	return {
		planModeEnabled: src.planModeEnabled === true,
		executionMode: src.executionMode === true,
		todos: todos
			.map((todo) => normalizeTodo(todo))
			.filter((todo) => todo.text.length > 0)
			.sort((a, b) => a.step - b.step),
	};
}

function normalizeTodo(input) {
	const src = typeof input === "object" && input !== null ? input : {};
	const step = Number.isFinite(src.step) ? Number(src.step) : parseInt(String(src.step || ""), 10);
	return {
		step: Number.isFinite(step) && step > 0 ? step : 0,
		text: String(src.text || "").trim(),
		completed: src.completed === true,
	};
}

function handleCommand(params, state) {
	const text = String(params.text || "").trim();
	if (!text.startsWith("/")) {
		return { handled: false, response: "" };
	}
	if (!text.toLowerCase().startsWith("/plan") && !text.toLowerCase().startsWith("/todos")) {
		return { handled: false, response: "" };
	}

	if (text.toLowerCase().startsWith("/todos")) {
		return {
			handled: true,
			response: formatTodos(state.todos),
		};
	}

	const parts = text.split(/\s+/);
	if (parts.length < 2) {
		return {
			handled: true,
			response: "usage: /plan on | /plan off | /plan status | /plan execute",
		};
	}

	const mode = parts[1].toLowerCase();
	switch (mode) {
		case "on":
			state.planModeEnabled = true;
			state.executionMode = false;
			return { handled: true, response: "plan mode: ON" };
		case "off":
			state.planModeEnabled = false;
			state.executionMode = false;
			return { handled: true, response: "plan mode: OFF" };
		case "status":
			return {
				handled: true,
				response: statusMessage(state),
			};
		case "execute":
			if (state.todos.length === 0) {
				return {
					handled: true,
					response: "no plan steps tracked yet; ask for a numbered plan first, then run /plan execute",
				};
			}
			state.planModeEnabled = false;
			state.executionMode = true;
			return {
				handled: true,
				response: `execution mode: ON (${remainingSteps(state.todos)} step(s) remaining)`,
			};
		default:
			return {
				handled: true,
				response: "usage: /plan on | /plan off | /plan status | /plan execute",
			};
	}
}

function statusMessage(state) {
	if (state.executionMode) {
		return `execution mode is ON (${remainingSteps(state.todos)} step(s) remaining)`;
	}
	if (state.planModeEnabled) {
		return "plan mode is ON";
	}
	return "plan mode is OFF";
}

function formatTodos(todos) {
	if (!Array.isArray(todos) || todos.length === 0) {
		return "no plan steps tracked yet";
	}
	const lines = todos
		.slice()
		.sort((a, b) => a.step - b.step)
		.map((todo) => `${todo.completed ? "✓" : "○"} ${todo.step}. ${todo.text}`);
	return ["plan steps:", ...lines].join("\n");
}

function remainingSteps(todos) {
	return todos.reduce((count, todo) => count + (todo.completed ? 0 : 1), 0);
}

function handleBeforeTurn(params, state) {
	const userText = String(params.user_text || "");
	if (state.planModeEnabled) {
		return {
			user_text: `${PLAN_PROMPT}\n\nUser request:\n${userText}`,
			system_prompt_append: "Plan mode is enabled for this session.",
		};
	}

	if (state.executionMode && state.todos.length > 0) {
		const remaining = state.todos.filter((todo) => !todo.completed);
		const remainingText = remaining.map((todo) => `${todo.step}. ${todo.text}`).join("\n");
		return {
			user_text: `${EXECUTION_PROMPT}\n\nRemaining steps:\n${remainingText}\n\nUser request:\n${userText}`,
			system_prompt_append: "Execution mode is enabled for this session.",
		};
	}

	return {
		user_text: userText,
		system_prompt_append: "",
	};
}

function handleAfterTurn(params, state) {
	const assistantText = String(params.assistant_text || "");

	if (state.planModeEnabled) {
		const extracted = extractTodos(assistantText);
		if (extracted.length > 0) {
			state.todos = extracted;
		}
	}

	if (state.executionMode && state.todos.length > 0) {
		const completed = markCompletedSteps(assistantText, state.todos);
		if (completed > 0 && remainingSteps(state.todos) === 0) {
			state.executionMode = false;
		}
	}

	return { ok: true };
}

function extractTodos(text) {
	const items = [];
	const re = /^\s*(\d+)[.)]\s+(.+?)\s*$/gm;
	let match;
	while ((match = re.exec(text)) !== null) {
		const step = parseInt(match[1], 10);
		const value = String(match[2] || "").trim();
		if (!Number.isFinite(step) || step <= 0 || value.length === 0) {
			continue;
		}
		items.push({
			step,
			text: value,
			completed: false,
		});
	}

	const deduped = [];
	const seen = new Set();
	for (const item of items) {
		if (seen.has(item.step)) {
			continue;
		}
		seen.add(item.step);
		deduped.push(item);
	}
	return deduped.sort((a, b) => a.step - b.step);
}

function markCompletedSteps(text, todos) {
	let changed = 0;
	const re = /\[DONE:(\d+)\]/gi;
	let match;
	while ((match = re.exec(text)) !== null) {
		const step = parseInt(match[1], 10);
		if (!Number.isFinite(step) || step <= 0) {
			continue;
		}
		for (const todo of todos) {
			if (todo.step === step && !todo.completed) {
				todo.completed = true;
				changed += 1;
			}
		}
	}
	return changed;
}
