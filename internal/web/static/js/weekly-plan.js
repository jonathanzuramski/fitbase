// Weekly plan panel on the calendar page: training-day / weekly-hours
// preferences, run "plan my week", stream the coach's review live, show the
// proposed week, accept / discard / revise it, and toggle the Sunday-night
// auto-plan. Needs coach-render.js (renderSafeMarkdown, parseSSE,
// markdownStreamer, escapeHTML).
(function () {
	const $ = (id) => document.getElementById(id);
	const auto = $('wp-auto'), next = $('wp-next'), intro = $('wp-intro');
	const notes = $('wp-notes'), runBtn = $('wp-run'), reviseBtn = $('wp-revise');
	const metaRow = $('wp-meta-row'), meta = $('wp-meta'), toggleBtn = $('wp-toggle');
	const status = $('wp-status'), review = $('wp-review'), week = $('wp-week');
	const actions = $('wp-actions'), acceptBtn = $('wp-accept'), discardBtn = $('wp-discard');
	const errEl = $('wp-error'), body = $('wp-body'), chip = $('wp-chip');
	const dayBoxes = Array.from(document.querySelectorAll('#wp-prefs .wp-day input'));
	const hoursInput = $('wp-hours'), savedEl = $('wp-prefs-saved');
	if (!runBtn) return;

	let current = null; // the plan on screen (null when none)
	let busy = false;

	const readyPlaceholder = 'change something? e.g. move the long ride to Sunday';
	const freshPlaceholder = notes.placeholder;

	function showError(msg) {
		errEl.textContent = msg || '';
		errEl.hidden = !msg;
	}

	// setChip labels the collapsed panel's header ("plan ready", "planning…")
	// so a plan waiting for review isn't missed while the panel is hidden.
	function setChip(text) {
		if (!chip) return;
		chip.textContent = text || '';
		chip.hidden = !text;
	}

	function setBusy(b) {
		busy = b;
		runBtn.disabled = reviseBtn.disabled = notes.disabled = b;
		acceptBtn.disabled = discardBtn.disabled = b;
		hoursInput.disabled = b;
		dayBoxes.forEach((box) => { box.disabled = b; });
	}

	// Plan dates are rider-local calendar days ("2026-11-09"); parse them as
	// local midnight so the weekday never shifts with the viewer's UTC offset.
	function fmtDay(iso) {
		const [y, m, d] = (iso || '').slice(0, 10).split('-').map(Number);
		if (!y) return '';
		return new Date(y, m - 1, d).toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' });
	}

	async function api(path, opts) {
		const res = await fetch(path, opts);
		let body = null;
		try { body = await res.json(); } catch (_) { }
		if (!res.ok) throw new Error((body && body.error) || ('HTTP ' + res.status));
		return body ? (body.data !== undefined ? body.data : body) : null;
	}

	// ── Preferences (training days, hours per week) ──────────────────────────

	function renderPrefs(p) {
		const days = (p && p.days) || [];
		dayBoxes.forEach((box) => { box.checked = days.includes(box.value); });
		hoursInput.value = p && p.weekly_hours != null ? p.weekly_hours : '';
	}

	let savedTimer = null;
	async function savePrefs() {
		const days = dayBoxes.filter((b) => b.checked).map((b) => b.value);
		if (days.length === 0) {
			showError('Pick at least one training day.');
			return false;
		}
		const raw = hoursInput.value.trim();
		let hours = null;
		if (raw !== '') {
			hours = Number(raw);
			if (!(hours >= 1 && hours <= 40)) {
				showError('Hours per week must be between 1 and 40 — or leave it blank and the coach decides.');
				return false;
			}
		}
		showError('');
		try {
			renderPrefs(await api('/api/coach/weekly-plan/preferences', {
				method: 'PUT',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ days: days, weekly_hours: hours }),
			}));
			savedEl.hidden = false;
			clearTimeout(savedTimer);
			savedTimer = setTimeout(() => { savedEl.hidden = true; }, 1500);
			return true;
		} catch (e) {
			showError('Could not save your training days — ' + e.message);
			return false;
		}
	}

	dayBoxes.forEach((box) => box.addEventListener('change', async () => {
		if (!(await savePrefs())) box.checked = !box.checked; // undo an invalid toggle
	}));
	hoursInput.addEventListener('change', savePrefs);

	// ── Schedule ──────────────────────────────────────────────────────────────

	function renderSchedule(s) {
		auto.checked = !!(s && s.enabled);
		if (s && s.enabled && s.next_run) {
			const when = new Date(s.next_run).toLocaleString(undefined, { weekday: 'long', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
			next.textContent = 'Next auto-plan: ' + when + '. You’ll find it here to review — nothing is added to your calendar until you accept it.';
			next.hidden = false;
		} else {
			next.hidden = true;
		}
	}

	// ── Plan ──────────────────────────────────────────────────────────────────

	function renderWeek(ws) {
		week.innerHTML = '';
		for (const p of ws) {
			const item = document.createElement('div');
			item.className = 'coach-preview-item';
			const dur = Math.round((p.duration_secs || 0) / 60);
			const tss = p.tss ? ' · ' + Math.round(p.tss) + ' TSS' : '';
			item.innerHTML =
				'<div class="coach-preview-date">' + escapeHTML(fmtDay(p.planned_date)) + ' · ' + escapeHTML(p.sport || 'cycling') + '</div>' +
				'<div class="coach-preview-main">' +
				'<span class="coach-preview-title-line">' + escapeHTML(p.title || '(untitled)') + '</span>' +
				'<span class="coach-preview-meta">' + dur + ' min' + tss + '</span>' +
				'</div>' +
				(p.description ? '<div class="coach-preview-desc">' + escapeHTML(p.description) + '</div>' : '');
			week.appendChild(item);
		}
		week.hidden = ws.length === 0;
	}

	function weekTotal(ws) {
		const mins = ws.reduce((s, p) => s + Math.round((p.duration_secs || 0) / 60), 0);
		return Math.floor(mins / 60) + 'h ' + String(mins % 60).padStart(2, '0') + 'm';
	}

	function renderPlan(plan, workouts) {
		current = plan;
		workouts = workouts || [];
		status.hidden = true;
		toggleBtn.hidden = true;
		body.classList.remove('wp-split');
		setChip(plan && plan.status === 'ready' ? 'plan ready' : '');
		if (!plan) {
			intro.hidden = false;
			metaRow.hidden = review.hidden = week.hidden = actions.hidden = reviseBtn.hidden = true;
			review.innerHTML = '';
			week.innerHTML = '';
			notes.placeholder = freshPlaceholder;
			return;
		}
		intro.hidden = true;
		const created = new Date(plan.created_at).toLocaleString(undefined, { weekday: 'short', month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
		const how = plan.trigger === 'scheduled' ? 'auto-planned ' : 'planned ';
		const span = fmtDay(plan.window_start) + ' – ' + fmtDay(plan.window_end);
		review.innerHTML = renderSafeMarkdown(plan.review_md || '');
		metaRow.hidden = false;

		if (plan.status === 'accepted') {
			// The week is on the calendar now; keep the review one click away
			// instead of leaving a full panel on screen.
			meta.textContent = span + ' · added to your calendar · ' + how + created;
			review.hidden = week.hidden = actions.hidden = reviseBtn.hidden = true;
			week.innerHTML = '';
			toggleBtn.hidden = !plan.review_md;
			toggleBtn.textContent = 'show review';
			notes.placeholder = freshPlaceholder;
			return;
		}

		meta.textContent = span + ' · ' + how + created + (workouts.length ? ' · ' + workouts.length + ' workouts, ' + weekTotal(workouts) : '');
		review.hidden = !plan.review_md;
		renderWeek(workouts);
		actions.hidden = workouts.length === 0;
		reviseBtn.hidden = false;
		// Wide screens: review and proposed week side by side (calendar.css).
		body.classList.toggle('wp-split', !!plan.review_md && workouts.length > 0);
		notes.placeholder = readyPlaceholder;
	}

	toggleBtn.addEventListener('click', () => {
		review.hidden = !review.hidden;
		toggleBtn.textContent = review.hidden ? 'show review' : 'hide review';
	});

	async function load() {
		const d = await api('/api/coach/weekly-plan');
		renderSchedule(d.schedule);
		renderPrefs(d.preferences);
		// A discarded plan is dismissed: show the empty state, not the old week.
		const plan = d.plan && d.plan.status !== 'discarded' ? d.plan : null;
		renderPlan(plan, d.workouts);
	}

	function setStatus(running) {
		const labels = Object.values(running);
		status.textContent = labels.length ? 'looking up ' + labels.join(', ') + '…' : 'thinking…';
	}

	// stream consumes one plan run's SSE — a new run or a re-attach to one that
	// is already going (including a Sunday-night auto-plan). The run itself is
	// detached server-side: leaving the page doesn't cancel it.
	async function stream(fetcher) {
		setBusy(true);
		showError('');
		intro.hidden = metaRow.hidden = week.hidden = actions.hidden = toggleBtn.hidden = true;
		body.classList.remove('wp-split');
		setChip('planning…');
		review.innerHTML = '';
		review.hidden = false;
		status.hidden = false;
		const running = {};
		setStatus(running);

		let md = '', streamErr = null;
		const streamer = markdownStreamer(review);
		try {
			const res = await fetcher();
			if (!res.ok || !res.body) {
				const j = await res.json().catch(() => ({}));
				throw new Error(j.error || 'request failed');
			}
			const reader = res.body.getReader();
			const decoder = new TextDecoder();
			let buf = '';
			while (true) {
				const { value, done } = await reader.read();
				if (done) break;
				buf += decoder.decode(value, { stream: true });
				buf = parseSSE(buf, (event, data) => {
					if (event === 'tool') {
						if (data.status === 'running') running[data.name] = data.label || (data.name || '').replace(/_/g, ' ');
						else delete running[data.name];
						setStatus(running);
					} else if (event === 'chunk' && data.text) {
						md += data.text;
						streamer.update(md);
					} else if (event === 'error') {
						streamErr = data.error || 'plan failed';
					}
				});
			}
			if (streamErr) throw new Error(streamErr);
		} catch (e) {
			showError('Could not plan your week — ' + e.message);
		} finally {
			streamer.stop(); // load() below renders the stored review
			setBusy(false);
			status.hidden = true;
			// The stored plan is the source of truth (review + proposed week).
			try { await load(); } catch (_) { }
		}
	}

	function startRun(body) {
		return stream(() => fetch('/api/coach/weekly-plan', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json', 'Accept': 'text/event-stream' },
			body: JSON.stringify(body),
		}));
	}

	runBtn.addEventListener('click', () => {
		if (busy) return;
		const n = notes.value.trim();
		notes.value = '';
		startRun({ notes: n });
	});

	reviseBtn.addEventListener('click', () => {
		if (busy || !current) return;
		const n = notes.value.trim();
		if (!n) {
			showError('Say what to change first — e.g. “move the long ride to Sunday”.');
			notes.focus();
			return;
		}
		notes.value = '';
		startRun({ notes: n, revise_id: current.id });
	});

	// Enter revises the plan on screen when there is one, else plans fresh.
	notes.addEventListener('keydown', (e) => {
		if (e.key !== 'Enter') return;
		e.preventDefault();
		(reviseBtn.hidden ? runBtn : reviseBtn).click();
	});

	acceptBtn.addEventListener('click', async () => {
		if (busy || !current) return;
		setBusy(true);
		showError('');
		try {
			await api('/api/coach/weekly-plan/' + encodeURIComponent(current.id) + '/accept', { method: 'POST' });
			location.reload(); // show the new workouts on the grid
		} catch (e) {
			showError('Could not add the week — ' + e.message);
			setBusy(false);
		}
	});

	discardBtn.addEventListener('click', async () => {
		if (busy || !current) return;
		setBusy(true);
		showError('');
		try {
			await api('/api/coach/weekly-plan/' + encodeURIComponent(current.id) + '/discard', { method: 'POST' });
			await load();
		} catch (e) {
			showError('Could not discard — ' + e.message);
		} finally {
			setBusy(false);
		}
	});

	auto.addEventListener('change', async () => {
		const want = auto.checked;
		showError('');
		try {
			renderSchedule(await api('/api/coach/weekly-plan/schedule', {
				method: 'PUT',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ enabled: want }),
			}));
		} catch (e) {
			auto.checked = !want;
			showError('Could not change the auto-plan setting — ' + e.message);
		}
	});

	// Re-attach to a run that is already going: one started before navigating
	// here, or the Sunday-night auto-plan in progress.
	async function resumeIfRunning() {
		try {
			const res = await fetch('/api/coach/weekly-plan/active');
			if (!res.ok || res.status === 204) return;
			const env = await res.json();
			const j = env.data || env;
			if (j && j.id && !j.done) {
				await stream(() => fetch('/api/coach/weekly-plan/runs/' + encodeURIComponent(j.id) + '/events', {
					headers: { 'Accept': 'text/event-stream' },
				}));
			}
		} catch (_) { }
	}

	load()
		.catch((e) => showError('Could not load your weekly plan — ' + e.message))
		.then(resumeIfRunning);
})();
