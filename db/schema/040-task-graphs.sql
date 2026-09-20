CREATE TABLE task_graphs (
    graph_id TEXT PRIMARY KEY,
    parent_conversation_id TEXT NOT NULL
        REFERENCES conversations(conversation_id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_task_graphs_parent_created
    ON task_graphs(parent_conversation_id, created_at DESC);

CREATE TABLE task_graph_tasks (
    graph_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    title TEXT NOT NULL,
    owner TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'queued',
    prompt TEXT,
    slug TEXT,
    model TEXT,
    reasoning TEXT,
    file_scopes TEXT NOT NULL DEFAULT '[]',
    child_conversation_id TEXT,
    final_response TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at DATETIME,
    completed_at DATETIME,
    PRIMARY KEY (graph_id, task_id),
    FOREIGN KEY (graph_id) REFERENCES task_graphs(graph_id) ON DELETE CASCADE,
    UNIQUE (child_conversation_id)
);

CREATE INDEX idx_task_graph_tasks_graph_status
    ON task_graph_tasks(graph_id, status, owner, task_id);

CREATE TABLE task_graph_dependencies (
    graph_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    depends_on_task_id TEXT NOT NULL,
    PRIMARY KEY (graph_id, task_id, depends_on_task_id),
    FOREIGN KEY (graph_id, task_id)
        REFERENCES task_graph_tasks(graph_id, task_id) ON DELETE CASCADE,
    FOREIGN KEY (graph_id, depends_on_task_id)
        REFERENCES task_graph_tasks(graph_id, task_id) ON DELETE CASCADE
);

CREATE INDEX idx_task_graph_dependencies_prerequisite
    ON task_graph_dependencies(graph_id, depends_on_task_id);
