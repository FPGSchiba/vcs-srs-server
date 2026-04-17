import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
    Box,
    Button,
    FormControl,
    InputLabel,
    MenuItem,
    Select,
    Typography,
} from '@mui/material';
import { Events } from '@wailsio/runtime';

interface LogEntry {
    time: string;
    level: string;
    msg: string;
    attrs?: Record<string, unknown>;
}

const MAX_ENTRIES = 500;

const LEVEL_COLORS: Record<string, string> = {
    DEBUG: 'text.disabled',
    INFO: 'text.primary',
    WARN: 'warning.main',
    ERROR: 'error.main',
};

type LevelFilter = 'ALL' | 'DEBUG' | 'INFO' | 'WARN' | 'ERROR';

const LEVEL_ORDER: Record<string, number> = { DEBUG: 0, INFO: 1, WARN: 2, ERROR: 3 };

function LogsPage() {
    const [entries, setEntries] = useState<LogEntry[]>([]);
    const [levelFilter, setLevelFilter] = useState<LevelFilter>('ALL');
    const [autoScroll, setAutoScroll] = useState(true);
    const bottomRef = useRef<HTMLDivElement>(null);
    const containerRef = useRef<HTMLDivElement>(null);

    const appendEntry = useCallback((entry: LogEntry) => {
        setEntries(prev => {
            const next = [...prev, entry];
            return next.length > MAX_ENTRIES ? next.slice(next.length - MAX_ENTRIES) : next;
        });
    }, []);

    useEffect(() => {
        Events.On('logs/entry', (event) => {
            const entry = event.data as LogEntry;
            appendEntry(entry);
        });
    }, [appendEntry]);

    useEffect(() => {
        if (autoScroll && bottomRef.current) {
            bottomRef.current.scrollIntoView({ behavior: 'smooth' });
        }
    }, [entries, autoScroll]);

    const handleScroll = () => {
        const container = containerRef.current;
        if (!container) return;
        const atBottom = container.scrollHeight - container.scrollTop <= container.clientHeight + 50;
        setAutoScroll(atBottom);
    };

    const filteredEntries = entries.filter(e => {
        if (levelFilter === 'ALL') return true;
        return LEVEL_ORDER[e.level] >= LEVEL_ORDER[levelFilter];
    });

    return (
        <Box sx={{ display: 'flex', flexDirection: 'column', height: '100%', gap: 1 }}>
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
                <Typography variant="h6">Logs</Typography>
                <FormControl size="small" sx={{ minWidth: 120 }}>
                    <InputLabel>Level</InputLabel>
                    <Select
                        value={levelFilter}
                        label="Level"
                        onChange={e => setLevelFilter(e.target.value as LevelFilter)}
                    >
                        <MenuItem value="ALL">All</MenuItem>
                        <MenuItem value="DEBUG">Debug+</MenuItem>
                        <MenuItem value="INFO">Info+</MenuItem>
                        <MenuItem value="WARN">Warn+</MenuItem>
                        <MenuItem value="ERROR">Error</MenuItem>
                    </Select>
                </FormControl>
                <Button
                    variant="outlined"
                    size="small"
                    onClick={() => setEntries([])}
                >
                    Clear
                </Button>
            </Box>
            <Box
                ref={containerRef}
                onScroll={handleScroll}
                sx={{
                    flex: 1,
                    overflowY: 'auto',
                    fontFamily: 'monospace',
                    fontSize: '0.8rem',
                    bgcolor: 'background.paper',
                    p: 1,
                    borderRadius: 1,
                }}
            >
                {filteredEntries.map((entry, i) => (
                    <Box key={i} sx={{ color: LEVEL_COLORS[entry.level] ?? 'text.primary', whiteSpace: 'pre-wrap', mb: 0.25 }}>
                        {`[${entry.time}] ${entry.level.padEnd(5)} ${entry.msg}`}
                        {entry.attrs && Object.keys(entry.attrs).length > 0
                            ? ' ' + JSON.stringify(entry.attrs)
                            : ''}
                    </Box>
                ))}
                <div ref={bottomRef} />
            </Box>
        </Box>
    );
}

export default LogsPage;
