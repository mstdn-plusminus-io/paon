import './public-path';
import { createRoot } from 'react-dom/client';

import Rails from '@rails/ujs';

import ready from '../mastodon/ready';

const setAnnouncementEndsAttributes = (target: HTMLInputElement) => {
  const valid = target.value && target.validity.valid;
  const element = document.querySelector<HTMLInputElement>(
    'input[type="datetime-local"]#announcement_ends_at',
  );

  if (!element) return;

  if (valid) {
    element.classList.remove('optional');
    element.required = true;
    element.min = target.value;
  } else {
    element.classList.add('optional');
    element.removeAttribute('required');
    element.removeAttribute('min');
  }
};

Rails.delegate(
  document,
  'input[type="datetime-local"]#announcement_starts_at',
  'change',
  ({ target }) => {
    if (target instanceof HTMLInputElement)
      setAnnouncementEndsAttributes(target);
  },
);

const batchCheckboxClassName = '.batch-checkbox input[type="checkbox"]';

const showSelectAll = () => {
  const selectAllMatchingElement = document.querySelector(
    '.batch-table__select-all',
  );
  selectAllMatchingElement?.classList.add('active');
};

const hideSelectAll = () => {
  const selectAllMatchingElement = document.querySelector(
    '.batch-table__select-all',
  );
  const hiddenField = document.querySelector<HTMLInputElement>(
    'input#select_all_matching',
  );
  const selectedMsg = document.querySelector(
    '.batch-table__select-all .selected',
  );
  const notSelectedMsg = document.querySelector(
    '.batch-table__select-all .not-selected',
  );

  selectAllMatchingElement?.classList.remove('active');
  selectedMsg?.classList.remove('active');
  notSelectedMsg?.classList.add('active');
  if (hiddenField) hiddenField.value = '0';
};

Rails.delegate(document, '#batch_checkbox_all', 'change', ({ target }) => {
  if (!(target instanceof HTMLInputElement)) return;

  const selectAllMatchingElement = document.querySelector(
    '.batch-table__select-all',
  );

  document
    .querySelectorAll<HTMLInputElement>(batchCheckboxClassName)
    .forEach((content) => {
      content.checked = target.checked;
    });

  if (selectAllMatchingElement) {
    if (target.checked) {
      showSelectAll();
    } else {
      hideSelectAll();
    }
  }
});

Rails.delegate(document, '.batch-table__select-all button', 'click', () => {
  const hiddenField = document.querySelector<HTMLInputElement>(
    '#select_all_matching',
  );

  if (!hiddenField) return;

  const active = hiddenField.value === '1';
  const selectedMsg = document.querySelector(
    '.batch-table__select-all .selected',
  );
  const notSelectedMsg = document.querySelector(
    '.batch-table__select-all .not-selected',
  );

  if (!selectedMsg || !notSelectedMsg) return;

  if (active) {
    hiddenField.value = '0';
    selectedMsg.classList.remove('active');
    notSelectedMsg.classList.add('active');
  } else {
    hiddenField.value = '1';
    notSelectedMsg.classList.remove('active');
    selectedMsg.classList.add('active');
  }
});

Rails.delegate(document, batchCheckboxClassName, 'change', () => {
  const checkAllElement = document.querySelector<HTMLInputElement>(
    'input#batch_checkbox_all',
  );
  const selectAllMatchingElement = document.querySelector(
    '.batch-table__select-all',
  );

  if (checkAllElement) {
    const allCheckboxes = Array.from(
      document.querySelectorAll<HTMLInputElement>(batchCheckboxClassName),
    );
    checkAllElement.checked = allCheckboxes.every((content) => content.checked);
    checkAllElement.indeterminate =
      !checkAllElement.checked &&
      allCheckboxes.some((content) => content.checked);

    if (selectAllMatchingElement) {
      if (checkAllElement.checked) {
        showSelectAll();
      } else {
        hideSelectAll();
      }
    }
  }
});

Rails.delegate(document, '.media-spoiler-show-button', 'click', () => {
  document
    .querySelectorAll<HTMLButtonElement>('button.media-spoiler')
    .forEach((element) => {
      element.click();
    });
});

Rails.delegate(document, '.media-spoiler-hide-button', 'click', () => {
  document
    .querySelectorAll<HTMLButtonElement>(
      '.spoiler-button.spoiler-button--visible button',
    )
    .forEach((element) => {
      element.click();
    });
});

Rails.delegate(
  document,
  '.filter-subset--with-select select',
  'change',
  ({ target }) => {
    if (target instanceof HTMLSelectElement) target.form?.submit();
  },
);

const onDomainBlockSeverityChange = (target: HTMLSelectElement) => {
  const rejectMediaDiv = document.querySelector(
    '.input.with_label.domain_block_reject_media',
  );
  const rejectReportsDiv = document.querySelector(
    '.input.with_label.domain_block_reject_reports',
  );

  if (rejectMediaDiv && rejectMediaDiv instanceof HTMLElement) {
    rejectMediaDiv.style.display =
      target.value === 'suspend' ? 'none' : 'block';
  }

  if (rejectReportsDiv && rejectReportsDiv instanceof HTMLElement) {
    rejectReportsDiv.style.display =
      target.value === 'suspend' ? 'none' : 'block';
  }
};

Rails.delegate(document, '#domain_block_severity', 'change', ({ target }) => {
  if (target instanceof HTMLSelectElement) onDomainBlockSeverityChange(target);
});

const onEnableBootstrapTimelineAccountsChange = (target: HTMLInputElement) => {
  const bootstrapTimelineAccountsField =
    document.querySelector<HTMLInputElement>(
      '#form_admin_settings_bootstrap_timeline_accounts',
    );

  if (bootstrapTimelineAccountsField) {
    bootstrapTimelineAccountsField.disabled = !target.checked;
    if (target.checked) {
      bootstrapTimelineAccountsField.parentElement?.classList.remove(
        'disabled',
      );
      bootstrapTimelineAccountsField.parentElement?.parentElement?.classList.remove(
        'disabled',
      );
    } else {
      bootstrapTimelineAccountsField.parentElement?.classList.add('disabled');
      bootstrapTimelineAccountsField.parentElement?.parentElement?.classList.add(
        'disabled',
      );
    }
  }
};

Rails.delegate(
  document,
  '#form_admin_settings_enable_bootstrap_timeline_accounts',
  'change',
  ({ target }) => {
    if (target instanceof HTMLInputElement)
      onEnableBootstrapTimelineAccountsChange(target);
  },
);

const onChangeRegistrationMode = (target: HTMLSelectElement) => {
  const enabled = target.value === 'approved';

  document
    .querySelectorAll<HTMLElement>(
      '.form_admin_settings_registrations_mode .warning-hint',
    )
    .forEach((warning_hint) => {
      warning_hint.style.display = target.value === 'open' ? 'inline' : 'none';
    });

  document
    .querySelectorAll<HTMLInputElement>(
      'input#form_admin_settings_require_invite_text',
    )
    .forEach((input) => {
      input.disabled = !enabled;
      if (enabled) {
        let element: HTMLElement | null = input;
        do {
          element.classList.remove('disabled');
          element = element.parentElement;
        } while (element && !element.classList.contains('fields-group'));
      } else {
        let element: HTMLElement | null = input;
        do {
          element.classList.add('disabled');
          element = element.parentElement;
        } while (element && !element.classList.contains('fields-group'));
      }
    });
};

const convertUTCDateTimeToLocal = (value: string) => {
  const date = new Date(value + 'Z');
  const twoChars = (x: number) => x.toString().padStart(2, '0');
  return `${date.getFullYear()}-${twoChars(date.getMonth() + 1)}-${twoChars(
    date.getDate(),
  )}T${twoChars(date.getHours())}:${twoChars(date.getMinutes())}`;
};

function convertLocalDatetimeToUTC(value: string) {
  const date = new Date(value);
  const fullISO8601 = date.toISOString();
  return fullISO8601.slice(0, fullISO8601.indexOf('T') + 6);
}

interface AsynqDashboardIssue {
  severity: string;
  code: string;
  label: string;
  detail: string;
  queue?: string;
  task_id?: string;
}

interface AsynqQueueView {
  name: string;
  display_name: string;
  size: number;
  pending?: number;
  active: number;
  retry: number;
  scheduled: number;
  archived: number;
  latency?: string;
  latency_seconds?: number;
  memory?: string | number;
  active_consumers?: number;
  failed_total?: number;
  status: string;
  status_label: string;
  issues: AsynqDashboardIssue[];
}

interface AsynqWorkerView {
  task_id: string;
  task_type: string;
  queue: string;
  started_at: string;
  elapsed: string;
  deadline?: string;
  orphaned: boolean;
}

interface AsynqServerView {
  id: string;
  host: string;
  pid?: number;
  status: string;
  concurrency: number;
  active: number;
  utilization: number;
  started_at: string;
  queues: string[];
  workers: AsynqWorkerView[];
}

interface AsynqHistoryView {
  date: string;
  processed: number;
  failed: number;
  succeeded: number;
}

interface AsynqDashboardSnapshot {
  timestamp: string;
  summary: Record<string, number>;
  queues: AsynqQueueView[];
  servers: AsynqServerView[];
  issues: AsynqDashboardIssue[];
  history: AsynqHistoryView[];
  error?: string | { message?: string };
}

const markdownCodeBlock = (value: string, language: string) => {
  let longestBacktickRun = 0;
  for (const match of value.matchAll(/`+/g))
    longestBacktickRun = Math.max(longestBacktickRun, match[0].length);
  const fence = '`'.repeat(Math.max(3, longestBacktickRun + 1));
  return `${fence}${language}\n${value}\n${fence}`;
};

const asynqTaskDetailsMarkdown = (
  dialog: HTMLDialogElement,
  content: HTMLElement,
) => {
  const title =
    dialog.querySelector('#asynq-task-modal-title')?.textContent?.trim() ??
    'Task details';
  const metadata = Array.from(
    content.querySelectorAll<HTMLElement>('[data-asynq-task-copy-metadata]'),
    (field) => {
      const label = field.querySelector('dt')?.textContent?.trim() ?? '';
      const value = field.querySelector('dd')?.textContent?.trim() ?? '';
      return `- **${label}:** ${value}`;
    },
  );
  const sections = Array.from(
    content.querySelectorAll<HTMLElement>('[data-asynq-task-copy-section]'),
    (section) => {
      const label =
        section
          .querySelector('[data-asynq-task-copy-label]')
          ?.textContent?.trim() ?? '';
      const value = section.querySelector('pre')?.textContent ?? '';
      const language =
        section.getAttribute('data-asynq-task-copy-language') ?? 'text';
      return `## ${label}\n\n${markdownCodeBlock(value, language)}`;
    },
  );
  return [`# ${title}`, metadata.join('\n'), ...sections].join('\n\n');
};

const copyText = async (value: string) => {
  const clipboard = (navigator as unknown as { clipboard?: Clipboard })
    .clipboard;
  if (clipboard) {
    await clipboard.writeText(value);
    return;
  }
  const textarea = document.createElement('textarea');
  textarea.value = value;
  textarea.setAttribute('readonly', '');
  textarea.style.position = 'fixed';
  textarea.style.opacity = '0';
  document.body.appendChild(textarea);
  try {
    textarea.select();
    if (!document.execCommand('copy')) throw new Error('Unable to copy text');
  } finally {
    textarea.remove();
  }
};

const initializeAsynqTaskDetails = (dashboard: HTMLElement) => {
  const dialog = dashboard.querySelector<HTMLDialogElement>(
    '[data-asynq-task-modal]',
  );
  const content = dialog?.querySelector<HTMLElement>(
    '[data-asynq-task-modal-content]',
  );
  const copyButton = dialog?.querySelector<HTMLButtonElement>(
    '[data-asynq-task-copy]',
  );
  if (!dialog || !content || !copyButton) return;

  let trigger: HTMLButtonElement | undefined;
  let copiedTimeout: number | undefined;
  dashboard.addEventListener('click', (event) => {
    if (!(event.target instanceof Element)) return;
    const button = event.target.closest<HTMLButtonElement>(
      '[data-asynq-task-details]',
    );
    if (!button || !dashboard.contains(button)) return;

    const template = button.parentElement?.querySelector<HTMLTemplateElement>(
      '[data-asynq-task-details-template]',
    );
    if (!template) return;

    trigger = button;
    content.replaceChildren(template.content.cloneNode(true));
    if (typeof dialog.showModal === 'function') {
      dialog.showModal();
    } else {
      dialog.setAttribute('open', '');
    }
  });

  copyButton.addEventListener('click', () => {
    void copyTaskDetails();
  });

  const copyTaskDetails = async () => {
    try {
      await copyText(asynqTaskDetailsMarkdown(dialog, content));
      window.clearTimeout(copiedTimeout);
      copyButton.classList.add('copied');
      const label = copyButton.querySelector('span');
      if (label)
        label.textContent = copyButton.getAttribute('data-copied-label');
      copiedTimeout = window.setTimeout(() => {
        copyButton.classList.remove('copied');
        if (label)
          label.textContent = copyButton.getAttribute('data-copy-label');
      }, 1500);
    } catch (error) {
      console.error('Unable to copy Asynq task details', error);
    }
  };

  dialog.addEventListener('click', (event) => {
    if (event.target === dialog) dialog.close();
  });
  dialog.addEventListener('close', () => {
    window.clearTimeout(copiedTimeout);
    copyButton.classList.remove('copied');
    const label = copyButton.querySelector('span');
    if (label) label.textContent = copyButton.getAttribute('data-copy-label');
    content.replaceChildren();
    trigger?.focus();
    trigger = undefined;
    dashboard.dispatchEvent(new CustomEvent('asynq:task-modal-closed'));
  });
};

const initializeAsynqPolling = (dashboard: HTMLElement) => {
  try {
    const currentURL = new URL(window.location.href);
    const hasFlash =
      currentURL.searchParams.has('notice') ||
      currentURL.searchParams.has('error');
    if (hasFlash) {
      currentURL.searchParams.delete('notice');
      currentURL.searchParams.delete('error');
      window.history.replaceState(
        window.history.state,
        '',
        `${currentURL.pathname}${currentURL.search}${currentURL.hash}`,
      );
    }
  } catch (error) {
    console.warn('Unable to clear Asynq action result from the URL', error);
  }

  const enabledInput = dashboard.querySelector<HTMLInputElement>(
    '#asynq_polling_enabled',
  );
  const intervalInput = dashboard.querySelector<HTMLInputElement>(
    '#asynq_polling_interval',
  );
  const intervalOutput = dashboard.querySelector<HTMLOutputElement>(
    '#asynq_polling_interval_value',
  );
  const summary = dashboard.querySelector<HTMLElement>('[data-asynq-summary]');
  const alerts = dashboard.querySelector<HTMLElement>('[data-asynq-alerts]');
  const queueBody = dashboard.querySelector<HTMLTableSectionElement>(
    '[data-asynq-queue-body]',
  );
  const serverBody = dashboard.querySelector<HTMLTableSectionElement>(
    '[data-asynq-server-body]',
  );
  const historyBody = dashboard.querySelector<HTMLTableSectionElement>(
    '[data-asynq-history-body]',
  );
  const historyRangeInput = dashboard.querySelector<HTMLSelectElement>(
    '#asynq_history_range',
  );
  const lastUpdated = dashboard.querySelector<HTMLElement>(
    '[data-asynq-last-updated]',
  );
  const errorMessage =
    dashboard.querySelector<HTMLElement>('[data-asynq-error]');
  const statsURL = dashboard.getAttribute('data-stats-url');
  const reloadOnPoll = dashboard.getAttribute('data-poll-reload') === 'true';

  if (
    !enabledInput ||
    !intervalInput ||
    !intervalOutput ||
    (!reloadOnPoll && !statsURL)
  )
    return;

  const enabledStorageKey = 'paon.asynq.polling.enabled';
  const intervalStorageKey = 'paon.asynq.polling.interval';
  const historyRangeStorageKey = 'paon.asynq.history.range';
  const pollingStatsURL = statsURL ?? '';
  const initialErrorMessage = errorMessage?.textContent?.trim();
  const numberFormatter = new Intl.NumberFormat(
    document.documentElement.lang || undefined,
  );
  const dateTimeFormatter = new Intl.DateTimeFormat(
    document.documentElement.lang || undefined,
    {
      dateStyle: 'medium',
      timeStyle: 'medium',
    },
  );
  let timeout: number | undefined;
  let latestHistory: AsynqHistoryView[] = [];
  let latestTimestamp: string | undefined;
  let reloadPending = false;

  try {
    enabledInput.checked = localStorage.getItem(enabledStorageKey) !== 'false';
    const storedInterval = Number.parseInt(
      localStorage.getItem(intervalStorageKey) ?? '',
      10,
    );
    if (storedInterval >= 2 && storedInterval <= 20)
      intervalInput.value = String(storedInterval);

    const storedHistoryRange = Number.parseInt(
      localStorage.getItem(historyRangeStorageKey) ?? '',
      10,
    );
    if (historyRangeInput && [7, 30, 90].includes(storedHistoryRange))
      historyRangeInput.value = String(storedHistoryRange);
  } catch (error) {
    console.warn('Unable to restore Asynq polling preferences', error);
  }

  const safeStatus = (value: unknown) => {
    const status = String(value || '').toLowerCase();
    return [
      'active',
      'blocked',
      'critical',
      'danger',
      'degraded',
      'error',
      'healthy',
      'idle',
      'info',
      'ok',
      'paused',
      'quiet',
      'saturated',
      'stale',
      'stopped',
      'warning',
    ].includes(status)
      ? status
      : 'unknown';
  };

  const text = (value: unknown) =>
    value === null || value === undefined || value === '' ? '—' : String(value);

  const formatNumber = (value: unknown) => {
    const number = Number(value);
    return Number.isFinite(number)
      ? numberFormatter.format(number)
      : text(value);
  };

  const createCell = (value: unknown, className?: string) => {
    const cell = document.createElement('td');
    if (className) cell.className = className;
    cell.textContent = text(value);
    return cell;
  };

  const replaceTableRows = (
    body: HTMLTableSectionElement,
    rows: HTMLTableRowElement[],
    columnCount: number,
  ) => {
    if (rows.length === 0 && dashboard.getAttribute('data-empty-label')) {
      const row = document.createElement('tr');
      const cell = createCell(
        dashboard.getAttribute('data-empty-label'),
        'asynq-table__empty',
      );
      cell.colSpan = columnCount;
      row.appendChild(cell);
      rows.push(row);
    }
    body.replaceChildren(...rows);
  };

  const createTime = (value: unknown) => {
    const time = document.createElement('time');
    const date = new Date(String(value ?? ''));
    if (value && !Number.isNaN(date.getTime())) {
      time.dateTime = date.toISOString();
      time.textContent = dateTimeFormatter.format(date);
    } else {
      time.textContent = text(value);
    }
    return time;
  };

  const updateIntervalOutput = () => {
    intervalOutput.textContent = `${intervalInput.value}s`;
  };

  const renderLastUpdated = (timestamp?: string, stale = false) => {
    if (!lastUpdated || !timestamp) return;

    const prefix =
      lastUpdated.getAttribute('data-label') ??
      dashboard.getAttribute('data-last-updated-label');
    const staleLabel =
      lastUpdated.getAttribute('data-stale-label') ??
      dashboard.getAttribute('data-stale-label');
    const fragment = document.createDocumentFragment();
    if (prefix) fragment.append(`${prefix} `);
    fragment.append(createTime(timestamp));
    if (stale && staleLabel) {
      const marker = document.createElement('strong');
      marker.className = 'asynq-dashboard__stale';
      marker.textContent = staleLabel;
      fragment.append(' ', marker);
    }
    lastUpdated.replaceChildren(fragment);
    lastUpdated.hidden = false;
  };

  const setError = (message?: string, stale = false) => {
    dashboard.classList.toggle('asynq-dashboard--stale', stale);
    if (errorMessage) {
      errorMessage.textContent = message ?? '';
      errorMessage.hidden = !message;
    }
    renderLastUpdated(latestTimestamp, stale);
  };

  const renderSummary = (values: Record<string, number>) => {
    if (!summary) return;

    summary.querySelectorAll('[data-asynq-counter]').forEach((counter) => {
      const key = counter.getAttribute('data-asynq-counter');
      if (key && Object.prototype.hasOwnProperty.call(values, key))
        counter.textContent = formatNumber(values[key]);
    });
  };

  const renderIssues = (issues: AsynqDashboardIssue[]) => {
    if (!alerts) return;

    const rows = issues.map((issue) => {
      const alert = document.createElement('div');
      const severity = safeStatus(issue.severity);
      alert.className = `asynq-alert asynq-alert--${severity}`;

      const label = document.createElement('strong');
      label.textContent = text(issue.label);
      alert.appendChild(label);

      if (issue.queue) {
        const queue = document.createElement('span');
        const queueLabel =
          dashboard.getAttribute('data-queue-label') ?? 'Queue';
        const queueName = document.createElement('code');
        queue.className = 'asynq-alert__queue';
        queue.append(`${queueLabel}: `, queueName);
        queueName.textContent = issue.queue;
        alert.appendChild(queue);
      }

      if (issue.detail) {
        const detail = document.createElement('span');
        detail.textContent = issue.detail;
        alert.appendChild(detail);
      }
      return alert;
    });

    alerts.replaceChildren(...rows);
    alerts.hidden = rows.length === 0;
  };

  const appendQueueStatus = (
    cell: HTMLTableCellElement,
    queue: AsynqQueueView,
  ) => {
    const badge = document.createElement('span');
    badge.className = `asynq-badge asynq-badge--${safeStatus(queue.status)}`;
    badge.textContent = text(queue.status_label || queue.status);
    cell.appendChild(badge);

    if (queue.active_consumers !== undefined) {
      const metadata = document.createElement('small');
      metadata.className = 'asynq-queue__metadata';
      const consumersLabel =
        dashboard.getAttribute('data-consumers-label') ?? 'Consumers';
      const failedLabel =
        dashboard.getAttribute('data-failed-label') ?? 'Failed';
      metadata.textContent = `${consumersLabel}: ${formatNumber(
        queue.active_consumers,
      )} · ${failedLabel}: ${formatNumber(queue.failed_total)}`;
      cell.appendChild(metadata);
    }

    if (Array.isArray(queue.issues) && queue.issues.length > 0) {
      const issues = document.createElement('ul');
      issues.className = 'asynq-queue__issues';
      queue.issues.forEach((issue) => {
        const item = document.createElement('li');
        item.className = `asynq-queue__issue asynq-queue__issue--${safeStatus(
          issue.severity,
        )}`;
        item.textContent = text(issue.label || issue);
        if (issue.detail) {
          const detail = document.createElement('span');
          detail.textContent = issue.detail;
          item.appendChild(detail);
        }
        issues.appendChild(item);
      });
      cell.appendChild(issues);
    }
  };

  const renderQueues = (queues: AsynqQueueView[]) => {
    if (!queueBody) return;

    const rows = queues.map((queue) => {
      const row = document.createElement('tr');
      row.dataset.queue = queue.name;
      if (Array.isArray(queue.issues) && queue.issues.length > 0)
        row.classList.add('asynq-table__row--attention');

      const nameCell = document.createElement('th');
      nameCell.scope = 'row';
      const displayName = document.createElement('strong');
      displayName.textContent = text(queue.display_name || queue.name);
      nameCell.appendChild(displayName);
      if (queue.display_name && queue.display_name !== queue.name) {
        const name = document.createElement('code');
        name.textContent = queue.name;
        nameCell.appendChild(name);
      }

      const statusCell = document.createElement('td');
      statusCell.className = 'asynq-queue__status';
      appendQueueStatus(statusCell, queue);

      row.append(
        nameCell,
        createCell(
          formatNumber(queue.pending ?? queue.size),
          'asynq-table__number',
        ),
        createCell(formatNumber(queue.active), 'asynq-table__number'),
        createCell(formatNumber(queue.retry), 'asynq-table__number'),
        createCell(formatNumber(queue.scheduled), 'asynq-table__number'),
        createCell(formatNumber(queue.archived), 'asynq-table__number'),
        createCell(
          queue.latency ??
            (queue.latency_seconds === undefined
              ? null
              : `${formatNumber(queue.latency_seconds)}s`),
          'asynq-table__number',
        ),
        createCell(
          typeof queue.memory === 'number'
            ? formatNumber(queue.memory)
            : queue.memory,
          'asynq-table__number',
        ),
        statusCell,
      );
      return row;
    });
    replaceTableRows(queueBody, rows, 9);
  };

  const appendWorkerMetadata = (
    container: HTMLElement,
    label: string,
    value: unknown,
    isTime = false,
  ) => {
    if (value === null || value === undefined || value === '') return;
    const item = document.createElement('span');
    if (label) item.append(`${label}: `);
    item.append(isTime ? createTime(value) : text(value));
    container.appendChild(item);
  };

  const renderWorkers = (
    cell: HTMLTableCellElement,
    workers: AsynqWorkerView[],
  ) => {
    if (!Array.isArray(workers) || workers.length === 0) {
      cell.textContent = '—';
      return;
    }

    const list = document.createElement('ul');
    list.className = 'asynq-workers';
    workers.forEach((worker) => {
      const item = document.createElement('li');
      item.className = 'asynq-worker';
      if (worker.orphaned) item.classList.add('asynq-worker--orphaned');

      const heading = document.createElement('div');
      heading.className = 'asynq-worker__heading';
      const type = document.createElement('strong');
      type.textContent = text(worker.task_type);
      heading.appendChild(type);
      if (worker.task_id) {
        const id = document.createElement('code');
        id.textContent = worker.task_id;
        heading.appendChild(id);
      }
      if (worker.orphaned) {
        const orphaned = document.createElement('span');
        orphaned.className = 'asynq-badge asynq-badge--error';
        orphaned.textContent =
          dashboard.getAttribute('data-orphaned-label') ?? 'orphaned';
        heading.appendChild(orphaned);
      }
      item.appendChild(heading);

      const metadata = document.createElement('div');
      metadata.className = 'asynq-worker__metadata';
      appendWorkerMetadata(
        metadata,
        dashboard.getAttribute('data-queue-label') ?? 'Queue',
        worker.queue,
      );
      appendWorkerMetadata(
        metadata,
        dashboard.getAttribute('data-started-label') ?? 'Started',
        worker.started_at,
        true,
      );
      appendWorkerMetadata(
        metadata,
        dashboard.getAttribute('data-elapsed-label') ?? 'Elapsed',
        worker.elapsed,
      );
      appendWorkerMetadata(
        metadata,
        dashboard.getAttribute('data-deadline-label') ?? 'Deadline',
        worker.deadline,
        true,
      );
      item.appendChild(metadata);
      list.appendChild(item);
    });
    cell.appendChild(list);
  };

  const renderServers = (servers: AsynqServerView[]) => {
    if (!serverBody) return;

    const rows = servers.map((server) => {
      const row = document.createElement('tr');
      row.dataset.server = server.id;

      const nameCell = document.createElement('th');
      nameCell.scope = 'row';
      const host = document.createElement('strong');
      host.textContent = text(server.host || server.id);
      nameCell.appendChild(host);
      if (server.id && server.id !== server.host) {
        const id = document.createElement('code');
        id.textContent = server.id;
        nameCell.appendChild(id);
      }
      if (server.pid !== undefined) {
        const pid = document.createElement('small');
        pid.textContent = `PID ${server.pid}`;
        nameCell.appendChild(pid);
      }
      if (server.started_at) {
        const startedAt = document.createElement('small');
        const startedLabel = dashboard.getAttribute('data-started-label');
        if (startedLabel) startedAt.append(`${startedLabel}: `);
        startedAt.appendChild(createTime(server.started_at));
        nameCell.appendChild(startedAt);
      }

      const statusCell = document.createElement('td');
      const status = document.createElement('span');
      status.className = `asynq-badge asynq-badge--${safeStatus(
        server.status,
      )}`;
      status.textContent = text(server.status);
      statusCell.appendChild(status);

      const utilization = [
        formatNumber(server.active),
        formatNumber(server.concurrency),
      ];
      if (Number.isFinite(server.utilization))
        utilization.push(`${formatNumber(server.utilization)}%`);

      const queuesCell = document.createElement('td');
      queuesCell.className = 'asynq-server__queues';
      if (Array.isArray(server.queues) && server.queues.length > 0) {
        server.queues.forEach((queue) => {
          const name = document.createElement('code');
          name.textContent = queue;
          queuesCell.appendChild(name);
        });
      } else {
        queuesCell.textContent = '—';
      }

      const workersCell = document.createElement('td');
      renderWorkers(workersCell, server.workers);
      row.append(
        nameCell,
        statusCell,
        createCell(formatNumber(server.concurrency), 'asynq-table__number'),
        createCell(utilization.join(' / '), 'asynq-table__number'),
        queuesCell,
        workersCell,
      );
      return row;
    });
    replaceTableRows(serverBody, rows, 6);
  };

  const renderHistoryValue = (value: number, kind: string, maximum: number) => {
    const cell = createCell(
      null,
      `asynq-history__value asynq-history__value--${kind}`,
    );
    const number = Number(value);
    const label = document.createElement('span');
    label.textContent = formatNumber(value);
    cell.replaceChildren(label);

    const bar = document.createElement('span');
    bar.className = 'asynq-history__bar';
    bar.setAttribute('aria-hidden', 'true');
    const ratio =
      Number.isFinite(number) && maximum > 0
        ? Math.max(0, number) / maximum
        : 0;
    bar.style.setProperty('--asynq-history-ratio', String(ratio));
    cell.appendChild(bar);
    return cell;
  };

  const renderHistory = () => {
    if (!historyBody) return;

    const requestedRange = Number.parseInt(historyRangeInput?.value ?? '', 10);
    const range = [7, 30, 90].includes(requestedRange) ? requestedRange : 7;
    const history = latestHistory.slice(-range);
    const maximum = history.reduce(
      (result, item) =>
        Math.max(
          result,
          Number(item.processed) || 0,
          Number(item.failed) || 0,
          Number(item.succeeded) || 0,
        ),
      0,
    );
    const rows = history.map((item) => {
      const row = document.createElement('tr');
      const date = document.createElement('th');
      date.scope = 'row';
      date.textContent = text(item.date);
      row.append(
        date,
        renderHistoryValue(item.processed, 'processed', maximum),
        renderHistoryValue(item.failed, 'failed', maximum),
        renderHistoryValue(item.succeeded, 'succeeded', maximum),
      );
      return row;
    });
    replaceTableRows(historyBody, rows, 4);
  };

  const renderPayload = (payload: AsynqDashboardSnapshot) => {
    renderSummary(payload.summary);
    if (Array.isArray(payload.issues)) renderIssues(payload.issues);
    if (Array.isArray(payload.queues)) renderQueues(payload.queues);
    if (Array.isArray(payload.servers)) renderServers(payload.servers);
    if (Array.isArray(payload.history)) {
      latestHistory = payload.history
        .slice()
        .sort((left, right) =>
          String(left.date).localeCompare(String(right.date)),
        );
      renderHistory();
    }
    dashboard
      .querySelector('[data-asynq-recovery-content]')
      ?.removeAttribute('hidden');

    const timestamp = new Date(payload.timestamp);
    if (!Number.isNaN(timestamp.getTime()))
      latestTimestamp = timestamp.toISOString();
    const staleAfter =
      Math.max(30, Number.parseInt(intervalInput.value, 10) * 3) * 1000;
    const stale = latestTimestamp
      ? Date.now() - new Date(latestTimestamp).getTime() > staleAfter
      : false;
    const payloadError =
      typeof payload.error === 'string'
        ? payload.error
        : payload.error?.message;
    setError(payloadError, Boolean(payloadError) || stale);
  };

  const schedule = () => {
    window.clearTimeout(timeout);
    if (enabledInput.checked)
      timeout = window.setTimeout(
        () => void poll(),
        Number.parseInt(intervalInput.value, 10) * 1000,
      );
  };

  const poll = async () => {
    if (reloadOnPoll) {
      const taskModal = dashboard.querySelector<HTMLDialogElement>(
        '[data-asynq-task-modal]',
      );
      if (taskModal?.open) {
        reloadPending = true;
        schedule();
        return;
      }
      window.location.reload();
      return;
    }

    dashboard.setAttribute('aria-busy', 'true');
    try {
      const response = await fetch(pollingStatsURL, {
        cache: 'no-store',
        headers: { Accept: 'application/json' },
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      renderPayload((await response.json()) as AsynqDashboardSnapshot);
    } catch (error) {
      console.error('Unable to refresh Asynq queue statistics', error);
      const refreshError =
        dashboard.getAttribute('data-refresh-error') ??
        initialErrorMessage ??
        (error instanceof Error ? error.message : String(error));
      setError(refreshError, true);
    } finally {
      dashboard.setAttribute('aria-busy', 'false');
      schedule();
    }
  };

  dashboard.addEventListener('asynq:task-modal-closed', () => {
    if (reloadOnPoll && reloadPending && enabledInput.checked)
      window.location.reload();
  });

  enabledInput.addEventListener('change', () => {
    try {
      localStorage.setItem(enabledStorageKey, enabledInput.checked.toString());
    } catch (error) {
      console.warn('Unable to save Asynq polling preference', error);
    }
    if (enabledInput.checked) {
      if (reloadOnPoll) schedule();
      else void poll();
    } else {
      reloadPending = false;
      window.clearTimeout(timeout);
    }
  });

  intervalInput.addEventListener('input', () => {
    updateIntervalOutput();
    try {
      localStorage.setItem(intervalStorageKey, intervalInput.value);
    } catch (error) {
      console.warn('Unable to save Asynq polling interval', error);
    }
    schedule();
  });

  historyRangeInput?.addEventListener('change', () => {
    try {
      localStorage.setItem(historyRangeStorageKey, historyRangeInput.value);
    } catch (error) {
      console.warn('Unable to save Asynq history range', error);
    }
    renderHistory();
  });

  updateIntervalOutput();
  dashboard.setAttribute('aria-busy', 'false');
  if (reloadOnPoll) schedule();
  else void poll();
};

Rails.delegate(
  document,
  '#form_admin_settings_registrations_mode',
  'change',
  ({ target }) => {
    if (target instanceof HTMLSelectElement) onChangeRegistrationMode(target);
  },
);

async function mountReactComponent(element: Element) {
  const componentName = element.getAttribute('data-admin-component');
  const stringProps = element.getAttribute('data-props');

  if (!stringProps) return;

  const componentProps = JSON.parse(stringProps) as object;

  const { default: AdminComponent } = await import(
    '../mastodon/containers/admin_component'
  );

  const { default: Component } = (await import(
    `../mastodon/components/admin/${componentName}`
  )) as { default: React.ComponentType };

  const root = createRoot(element);

  root.render(
    <AdminComponent>
      <Component {...componentProps} />
    </AdminComponent>,
  );
}

ready(() => {
  const domainBlockSeveritySelect = document.querySelector<HTMLSelectElement>(
    'select#domain_block_severity',
  );
  if (domainBlockSeveritySelect)
    onDomainBlockSeverityChange(domainBlockSeveritySelect);

  const enableBootstrapTimelineAccounts =
    document.querySelector<HTMLInputElement>(
      'input#form_admin_settings_enable_bootstrap_timeline_accounts',
    );
  if (enableBootstrapTimelineAccounts)
    onEnableBootstrapTimelineAccountsChange(enableBootstrapTimelineAccounts);

  const registrationMode = document.querySelector<HTMLSelectElement>(
    'select#form_admin_settings_registrations_mode',
  );
  if (registrationMode) onChangeRegistrationMode(registrationMode);

  document
    .querySelectorAll<HTMLElement>('[data-asynq-dashboard]')
    .forEach((dashboard) => {
      initializeAsynqTaskDetails(dashboard);
      initializeAsynqPolling(dashboard);
    });

  const checkAllElement = document.querySelector<HTMLInputElement>(
    'input#batch_checkbox_all',
  );
  if (checkAllElement) {
    const allCheckboxes = Array.from(
      document.querySelectorAll<HTMLInputElement>(batchCheckboxClassName),
    );
    checkAllElement.checked = allCheckboxes.every((content) => content.checked);
    checkAllElement.indeterminate =
      !checkAllElement.checked &&
      allCheckboxes.some((content) => content.checked);
  }

  document
    .querySelector('a#add-instance-button')
    ?.addEventListener('click', (e) => {
      const domain = document.querySelector<HTMLInputElement>(
        'input[type="text"]#by_domain',
      )?.value;

      if (domain && e.target instanceof HTMLAnchorElement) {
        const url = new URL(e.target.href);
        url.searchParams.set('_domain', domain);
        e.target.href = url.toString();
      }
    });

  document
    .querySelectorAll<HTMLInputElement>('input[type="datetime-local"]')
    .forEach((element) => {
      if (element.value) {
        element.value = convertUTCDateTimeToLocal(element.value);
      }
      if (element.placeholder) {
        element.placeholder = convertUTCDateTimeToLocal(element.placeholder);
      }
    });

  Rails.delegate(document, 'form', 'submit', ({ target }) => {
    if (target instanceof HTMLFormElement)
      target
        .querySelectorAll<HTMLInputElement>('input[type="datetime-local"]')
        .forEach((element) => {
          if (element.value && element.validity.valid) {
            element.value = convertLocalDatetimeToUTC(element.value);
          }
        });
  });

  const announcementStartsAt = document.querySelector<HTMLInputElement>(
    'input[type="datetime-local"]#announcement_starts_at',
  );
  if (announcementStartsAt) {
    setAnnouncementEndsAttributes(announcementStartsAt);
  }

  document.querySelectorAll('[data-admin-component]').forEach((element) => {
    void mountReactComponent(element);
  });
}).catch((reason: unknown) => {
  throw reason;
});
