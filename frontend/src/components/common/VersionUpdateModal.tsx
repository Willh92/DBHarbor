import React, { useEffect, useState } from 'react';
import {
  Alert,
  Badge,
  Button,
  Card,
  Empty,
  Modal,
  Segmented,
  Space,
  Spin,
  Tabs,
  Tag,
  Typography,
  message,
} from 'antd';
import {
  CheckCircleOutlined,
  CloudDownloadOutlined,
  FileTextOutlined,
  GithubOutlined,
  GlobalOutlined,
  ReloadOutlined,
  RocketOutlined,
  SyncOutlined,
} from '@ant-design/icons';
import { Switch } from 'antd';
import {
  api,
  getAutoUpdateRemindEnabled,
  getSavedUpdateChannel,
  getSkippedUpdateVersion,
  setAutoUpdateRemindEnabled,
  setSavedUpdateChannel,
  setSkippedUpdateVersion,
} from '../../api/client';
import { LOCALE_OPTIONS, type AppLocale } from '../../i18n';
import { useResponsive } from '../../hooks/useResponsive';
import { useI18n } from '../../i18n/useI18n';
import type { BuildVersionInfo, UpdateChannel, UpdateCheckResult } from '../../types';
import { formatBytes } from '../../utils/format';

interface Props {
  open: boolean;
  initialBuildInfo?: BuildVersionInfo | null;
  initialCheckResult?: UpdateCheckResult | null;
  onClose: () => void;
  onUpdateChecked?: (res: UpdateCheckResult) => void;
  onRemindSettingChanged?: (enabled: boolean) => void;
}

const SOURCE_LABELS: Record<string, { text: string; color: string }> = {
  github_changelog: { text: 'Synced from GitHub CHANGELOG.md', color: 'green' },
  github_releases: { text: 'Synced from GitHub Releases', color: 'blue' },
  github_commits: { text: 'Synced from GitHub Commits', color: 'cyan' },
  embedded: { text: 'Built-in Local Changelog', color: 'default' },
};

function resolveRelativeChangelogLang(href: string): AppLocale | null {
  const lower = href.toLowerCase();
  if (lower.includes('changelog.zh-cn.md')) return 'zh-CN';
  if (lower.includes('changelog.zh-tw.md')) return 'zh-TW';
  if (lower.includes('changelog.ja.md')) return 'ja-JP';
  if (lower.endsWith('changelog.md')) return 'en-US';
  return null;
}

/** Lightweight zero-dependency Markdown renderer tailored for CHANGELOG.md & GitHub Release Notes */
const SimpleMarkdownView: React.FC<{
  markdown: string;
  onSwitchLang?: (lang: AppLocale) => void;
}> = ({ markdown, onSwitchLang }) => {
  const lines = (markdown || '')
    .replace(/\r\n/g, '\n')
    .split('\n')
    // Hide the GitHub file-navigation switcher row since a native language selector is provided in the modal header
    .filter((ln) => !/\.\/CHANGELOG(\.[a-zA-Z-]+)?\.md/i.test(ln));

  return (
    <div style={{ lineHeight: 1.75, fontSize: 13 }}>
      {lines.map((raw, idx) => {
        const line = raw.trimEnd();
        if (!line.trim()) {
          return <div key={idx} style={{ height: 6 }} />;
        }
        if (/^---+$/.test(line.trim())) {
          return (
            <hr
              key={idx}
              style={{ border: 'none', borderTop: '1px solid rgba(128,128,128,0.2)', margin: '12px 0' }}
            />
          );
        }
        if (line.startsWith('# ')) {
          return (
            <Typography.Title key={idx} level={4} style={{ marginTop: 2, marginBottom: 10 }}>
              {renderInlineMarkdown(line.slice(2), onSwitchLang)}
            </Typography.Title>
          );
        }
        if (line.startsWith('## ')) {
          return (
            <Typography.Title
              key={idx}
              level={5}
              style={{
                marginTop: 14,
                marginBottom: 8,
                paddingBottom: 4,
                borderBottom: '1px solid rgba(22,119,255,0.18)',
              }}
            >
              {renderInlineMarkdown(line.slice(3), onSwitchLang)}
            </Typography.Title>
          );
        }
        if (line.startsWith('### ')) {
          return (
            <Typography.Text
              key={idx}
              strong
              style={{ display: 'block', marginTop: 10, marginBottom: 4, color: '#1677ff' }}
            >
              {renderInlineMarkdown(line.slice(4), onSwitchLang)}
            </Typography.Text>
          );
        }
        if (line.trimStart().startsWith('- ') || line.trimStart().startsWith('* ')) {
          const indentLevel = Math.floor((line.length - line.trimStart().length) / 2);
          const content = line.trimStart().slice(2);
          return (
            <div
              key={idx}
              style={{ paddingLeft: 12 + indentLevel * 16, display: 'flex', gap: 8, marginBottom: 4 }}
            >
              <span style={{ color: '#1677ff', fontWeight: 700 }}>•</span>
              <span style={{ flex: 1, minWidth: 0 }}>{renderInlineMarkdown(content, onSwitchLang)}</span>
            </div>
          );
        }
        return (
          <div key={idx} style={{ marginBottom: 4 }}>
            {renderInlineMarkdown(line, onSwitchLang)}
          </div>
        );
      })}
    </div>
  );
};

function renderInlineMarkdown(
  text: string,
  onSwitchLang?: (lang: AppLocale) => void
): React.ReactNode {
  const parts: React.ReactNode[] = [];
  const regex = /(\[([^\]]+)\]\(([^)\s]+)\)|\*\*([^*]+)\*\*|`([^`]+)`)/g;
  let lastIdx = 0;
  let match: RegExpExecArray | null;
  let k = 0;

  while ((match = regex.exec(text)) !== null) {
    if (match.index > lastIdx) {
      parts.push(text.slice(lastIdx, match.index));
    }
    if (match[2] && match[3]) {
      const href = match[3];
      const relLang = resolveRelativeChangelogLang(href);
      if (relLang && onSwitchLang) {
        parts.push(
          <a
            key={`lnk_${k++}`}
            onClick={(e) => {
              e.preventDefault();
              onSwitchLang(relLang);
            }}
          >
            {match[2]}
          </a>
        );
      } else if (/^https?:\/\//i.test(href)) {
        parts.push(
          <Typography.Link key={`lnk_${k++}`} href={href} target="_blank" rel="noreferrer">
            {match[2]}
          </Typography.Link>
        );
      } else {
        parts.push(match[2]);
      }
    } else if (match[4]) {
      parts.push(<strong key={`b_${k++}`}>{match[4]}</strong>);
    } else if (match[5]) {
      parts.push(
        <Typography.Text key={`c_${k++}`} code style={{ fontSize: 12 }}>
          {match[5]}
        </Typography.Text>
      );
    }
    lastIdx = regex.lastIndex;
  }
  if (lastIdx < text.length) {
    parts.push(text.slice(lastIdx));
  }
  return parts.length > 0 ? parts : text;
}

export const VersionUpdateModal: React.FC<Props> = ({
  open,
  initialBuildInfo,
  initialCheckResult,
  onClose,
  onUpdateChecked,
  onRemindSettingChanged,
}) => {
  const { locale, t } = useI18n();
  const { isMobile } = useResponsive();
  const [activeTab, setActiveTab] = useState<string>('changelog');
  const [buildInfo, setBuildInfo] = useState<BuildVersionInfo | null>(initialBuildInfo || null);
  const [checkData, setCheckData] = useState<UpdateCheckResult | null>(initialCheckResult || null);
  // Default changelog language strictly prioritizes the auto-detected / active UI locale
  const [changelogLang, setChangelogLang] = useState<AppLocale>(locale);
  const [channel, setChannel] = useState<UpdateChannel>(
    () =>
      getSavedUpdateChannel() ||
      (initialCheckResult?.channel === 'preview' || initialBuildInfo?.channel === 'preview' ? 'preview' : 'release')
  );
  const [checking, setChecking] = useState(false);
  const [autoRemind, setAutoRemind] = useState<boolean>(() => getAutoUpdateRemindEnabled());
  const [skippedVer, setSkippedVer] = useState<string>(() => getSkippedUpdateVersion());

  const runCheck = async (force: boolean, targetLang?: AppLocale, targetChannel?: UpdateChannel) => {
    const ch = targetChannel || channel;
    const lg = targetLang || changelogLang;
    setChecking(true);
    try {
      const [ver, upd] = await Promise.all([api.getVersion(lg), api.checkUpdate(force, lg, ch)]);
      setBuildInfo(ver);
      setCheckData(upd);
      if (!getSavedUpdateChannel() && (upd.channel === 'preview' || upd.channel === 'release')) {
        setChannel(upd.channel);
      }
      if (lg === locale) {
        onUpdateChecked?.(upd);
      }
      if (force) {
        if (upd.hasUpdate) {
          message.info(t('New GitHub version detected: {ver}', { ver: upd.latestVersion }));
        } else if (upd.onlineCheckSuccess) {
          message.success(t('Already up to date ({ver})', { ver: upd.currentVersion }));
        } else {
          message.warning(
            upd.notice ? t(upd.notice) : t('Unable to reach GitHub; showing built-in local changelog')
          );
        }
      }
    } catch (err: any) {
      message.error(err.message || t('Failed to check for updates'));
    } finally {
      setChecking(false);
    }
  };

  const handleChannelChange = (val: string | number) => {
    const nextChannel: UpdateChannel = val === 'preview' ? 'preview' : 'release';
    setChannel(nextChannel);
    setSavedUpdateChannel(nextChannel);
    runCheck(false, changelogLang, nextChannel);
  };

  const handleChangelogLangChange = (val: string | number) => {
    const nextLang = val as AppLocale;
    setChangelogLang(nextLang);
    // Immediate 0ms switch using built-in localized changelog map while syncing online in background
    const instantLocal = buildInfo?.embeddedChangelogs?.[nextLang];
    if (instantLocal && checkData?.changelogSource === 'embedded') {
      setCheckData((prev) =>
        prev ? { ...prev, changelogLang: nextLang, changelogMarkdown: instantLocal } : prev
      );
    }
    runCheck(false, nextLang, channel);
  };

  useEffect(() => {
    if (open) {
      setChangelogLang(locale);
      setAutoRemind(getAutoUpdateRemindEnabled());
      setSkippedVer(getSkippedUpdateVersion());
      if (initialBuildInfo && !buildInfo) setBuildInfo(initialBuildInfo);
      runCheck(false, locale, channel);
    }
  }, [open, locale]);

  const srcBadge = SOURCE_LABELS[checkData?.changelogSource || 'embedded'] || SOURCE_LABELS.embedded;
  const repoUrl = checkData?.repoUrl || buildInfo?.repoUrl || 'https://github.com/Willh92/DBHarbor';
  const activeMarkdown =
    (checkData?.changelogSource === 'embedded' && buildInfo?.embeddedChangelogs?.[changelogLang]) ||
    checkData?.changelogMarkdown ||
    buildInfo?.embeddedChangelogs?.[changelogLang] ||
    buildInfo?.embeddedChangelog ||
    t('No changelog available');

  return (
    <Modal
      open={open}
      title={
        <Space>
          <RocketOutlined style={{ color: '#1677ff' }} />
          <span>{t('Version Info, GitHub Update Check & Changelog')}</span>
        </Space>
      }
      width="min(820px, calc(100vw - 24px))"
      onCancel={onClose}
      footer={null}
      destroyOnClose
    >
      <Spin spinning={checking && !checkData}>
        <Card size="small" style={{ marginBottom: 12 }}>
          {/* Row 1: Primary Version Badges + Channel & Check Actions */}
          <div
            style={{
              display: 'flex',
              justifyContent: 'space-between',
              alignItems: 'center',
              flexWrap: 'wrap',
              gap: 10,
            }}
          >
            <Space size={[6, 6]} wrap>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t('Current Version')}:
              </Typography.Text>
              <Tag color="geekblue" style={{ fontWeight: 700, margin: 0 }}>
                {checkData?.currentVersion || buildInfo?.displayVersion || 'v1.0.0'}
              </Tag>
              {buildInfo?.edition === 'desktop' && (
                <Tag color="purple" style={{ margin: 0 }}>
                  {t('Local Desktop')}
                </Tag>
              )}
              {buildInfo?.channel === 'preview' && (
                <Tag color="orange" style={{ margin: 0 }}>
                  {t('Preview')}
                </Tag>
              )}

              {!isMobile && <span style={{ opacity: 0.35, margin: '0 2px' }}>|</span>}

              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t('Latest on GitHub')}:
              </Typography.Text>
              {checkData ? (
                <>
                  <Tag color={checkData.hasUpdate ? 'volcano' : 'success'} style={{ fontWeight: 700, margin: 0 }}>
                    {checkData.latestVersion}
                  </Tag>
                  {checkData.latestPrerelease && (
                    <Tag color="orange" style={{ margin: 0 }}>
                      {t('Preview')}
                    </Tag>
                  )}
                  {checkData.hasUpdate ? (
                    <Badge status="processing" text={t('Update Available')} />
                  ) : (
                    <Tag icon={<CheckCircleOutlined />} color="success" style={{ margin: 0 }}>
                      {t('Up to Date')}
                    </Tag>
                  )}
                </>
              ) : (
                <span>-</span>
              )}
            </Space>

            <Space size={6} wrap>
              <Segmented
                size="small"
                value={channel}
                onChange={handleChannelChange}
                disabled={checking}
                options={[
                  { label: t('Release (Stable)'), value: 'release' },
                  { label: t('Preview'), value: 'preview' },
                ]}
              />
              <Button
                size="small"
                type="primary"
                icon={checking ? <SyncOutlined spin /> : <ReloadOutlined />}
                loading={checking}
                onClick={() => runCheck(true, changelogLang, channel)}
              >
                {t('Check GitHub Updates Now')}
              </Button>
              <Button size="small" icon={<GithubOutlined />} href={repoUrl} target="_blank" rel="noreferrer">
                {t('Open GitHub Repo')}
              </Button>
            </Space>
          </div>

          {/* Row 2: Clean Non-Overlapping CSS Grid Metadata Strip */}
          <div
            style={{
              display: 'grid',
              gridTemplateColumns: isMobile ? '1fr' : 'repeat(auto-fit, minmax(220px, 1fr))',
              gap: isMobile ? '6px 12px' : '8px 18px',
              marginTop: 10,
              paddingTop: 10,
              borderTop: '1px solid rgba(128,128,128,0.14)',
              fontSize: 12,
            }}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
              <Typography.Text type="secondary" style={{ whiteSpace: 'nowrap' }}>
                {t('Platform / Runtime')}:
              </Typography.Text>
              <Typography.Text ellipsis style={{ minWidth: 0 }}>
                {buildInfo ? `${buildInfo.os}/${buildInfo.arch} (${buildInfo.goVersion})` : '-'}
              </Typography.Text>
            </div>

            <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
              <Typography.Text type="secondary" style={{ whiteSpace: 'nowrap' }}>
                {t('License')}:
              </Typography.Text>
              <Tag color="cyan" style={{ margin: 0 }}>
                {buildInfo?.license || 'Apache-2.0'}
              </Tag>
            </div>

            <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
              <Typography.Text type="secondary" style={{ whiteSpace: 'nowrap' }}>
                {t('Changelog Source')}:
              </Typography.Text>
              <Tag color={srcBadge.color} style={{ margin: 0 }}>
                {t(srcBadge.text)}
              </Tag>
            </div>

            <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
              <Typography.Text type="secondary" style={{ whiteSpace: 'nowrap' }}>
                {t('Last Checked')}:
              </Typography.Text>
              <Typography.Text ellipsis style={{ minWidth: 0 }}>
                {checkData?.checkedAt ? new Date(checkData.checkedAt).toLocaleString() : '-'}
              </Typography.Text>
            </div>

            <div style={{ display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
              <Typography.Text type="secondary" style={{ whiteSpace: 'nowrap' }}>
                {t('Auto Update Reminder')}:
              </Typography.Text>
              <Switch
                size="small"
                checked={autoRemind}
                onChange={(checked) => {
                  setAutoRemind(checked);
                  setAutoUpdateRemindEnabled(checked);
                  onRemindSettingChanged?.(checked);
                  message.success(
                    checked
                      ? t('Automatic update reminder enabled')
                      : t('Automatic update reminder disabled')
                  );
                }}
              />
            </div>

            <div style={{ gridColumn: '1 / -1', display: 'flex', alignItems: 'center', gap: 6, minWidth: 0 }}>
              <Typography.Text type="secondary" style={{ whiteSpace: 'nowrap' }}>
                {t('Official GitHub Repo')}:
              </Typography.Text>
              <Typography.Link
                href={repoUrl}
                target="_blank"
                rel="noreferrer"
                ellipsis
                style={{ minWidth: 0 }}
              >
                <GithubOutlined /> {repoUrl}
              </Typography.Link>
            </div>
          </div>

          {checkData?.hasUpdate && (
            <Alert
              type="info"
              showIcon
              style={{ marginTop: 10 }}
              message={t('New version {ver} is available for upgrade!', { ver: checkData.latestVersion })}
              description={
                <div
                  style={{
                    display: 'flex',
                    justifyContent: 'space-between',
                    alignItems: 'center',
                    flexWrap: 'wrap',
                    gap: 8,
                    marginTop: 4,
                  }}
                >
                  <span>
                    {checkData.matchedAssetName
                      ? t('Matched installer for your platform: {name}', { name: checkData.matchedAssetName })
                      : t('Visit GitHub Releases to download the standalone binary for your OS.')}
                  </span>
                  <Space wrap>
                    {checkData.matchedAssetUrl && (
                      <Button
                        size="small"
                        type="primary"
                        icon={<CloudDownloadOutlined />}
                        href={checkData.matchedAssetUrl}
                        target="_blank"
                        rel="noreferrer"
                      >
                        {t('Download for Current OS ({name})', { name: checkData.matchedAssetName || '' })}
                      </Button>
                    )}
                    <Button size="small" href={checkData.releaseUrl} target="_blank" rel="noreferrer">
                      {t('Release Details')}
                    </Button>
                    {skippedVer === checkData.latestVersion ? (
                      <Button
                        size="small"
                        onClick={() => {
                          setSkippedUpdateVersion('');
                          setSkippedVer('');
                          message.info(t('Restored reminder for {ver}', { ver: checkData.latestVersion }));
                        }}
                      >
                        {t('Skipped ({ver})', { ver: checkData.latestVersion })}
                      </Button>
                    ) : (
                      <Button
                        size="small"
                        onClick={() => {
                          setSkippedUpdateVersion(checkData.latestVersion);
                          setSkippedVer(checkData.latestVersion);
                          message.info(t('Skipped reminder for {ver}', { ver: checkData.latestVersion }));
                        }}
                      >
                        {t('Skip This Version')}
                      </Button>
                    )}
                  </Space>
                </div>
              }
            />
          )}

          {checkData?.notice && (
            <Alert
              type="warning"
              showIcon
              style={{ marginTop: 10 }}
              message={t(checkData.notice)}
            />
          )}
        </Card>

        <Tabs
          size="small"
          activeKey={activeTab}
          onChange={setActiveTab}
          tabBarGutter={isMobile ? 16 : undefined}
          tabBarExtraContent={
            !isMobile && activeTab === 'changelog' ? (
              <Space size={6}>
                <GlobalOutlined style={{ opacity: 0.65, fontSize: 12 }} />
                <Segmented
                  size="small"
                  value={changelogLang}
                  onChange={handleChangelogLangChange}
                  options={LOCALE_OPTIONS.map((opt) => ({
                    value: opt.value,
                    label: opt.short,
                  }))}
                />
              </Space>
            ) : null
          }
          items={[
            {
              key: 'changelog',
              label: (
                <Space size={6}>
                  <FileTextOutlined />
                  <span>{isMobile ? t('Changelog') : t('Changelog (CHANGELOG)')}</span>
                </Space>
              ),
              children: (
                <div>
                  {isMobile && (
                    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 6, marginBottom: 8 }}>
                      <Space size={4}>
                        <GlobalOutlined style={{ opacity: 0.65, fontSize: 12 }} />
                        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                          {t('Language')}
                        </Typography.Text>
                      </Space>
                      <Segmented
                        size="small"
                        value={changelogLang}
                        onChange={handleChangelogLangChange}
                        options={LOCALE_OPTIONS.map((opt) => ({
                          value: opt.value,
                          label: opt.short,
                        }))}
                      />
                    </div>
                  )}
                  <div
                    style={{
                      maxHeight: 'min(380px, calc(100vh - 330px))',
                      minHeight: 140,
                      overflowY: 'auto',
                      padding: isMobile ? '10px 12px' : '12px 16px',
                      borderRadius: 8,
                      border: '1px solid rgba(128,128,128,0.18)',
                      background: 'rgba(128,128,128,0.04)',
                    }}
                  >
                    <SimpleMarkdownView
                      markdown={activeMarkdown}
                      onSwitchLang={(lg) => handleChangelogLangChange(lg)}
                    />
                  </div>
                </div>
              ),
            },
            {
              key: 'releases',
              label: (
                <Space size={6}>
                  <GithubOutlined />
                  <span>GitHub Releases ({checkData?.releases?.length || 0})</span>
                </Space>
              ),
              children:
                (checkData?.releases?.length || 0) === 0 ? (
                  <Empty
                    image={Empty.PRESENTED_IMAGE_SIMPLE}
                    description={
                      <span>
                        {t('No GitHub Releases published yet. Visit')}{' '}
                        <Typography.Link href={`${repoUrl}/releases`} target="_blank" rel="noreferrer">
                          Willh92/DBHarbor Releases
                        </Typography.Link>{' '}
                        {t('for details.')}
                      </span>
                    }
                  />
                ) : (
                  <div
                    style={{
                      maxHeight: 'min(380px, calc(100vh - 330px))',
                      minHeight: 140,
                      overflowY: 'auto',
                      display: 'flex',
                      flexDirection: 'column',
                      gap: 10,
                    }}
                  >
                    {checkData!.releases.map((rel) => (
                      <Card
                        key={rel.tagName}
                        size="small"
                        title={
                          <Space wrap>
                            <Tag color={rel.prerelease ? 'orange' : 'blue'}>{rel.tagName}</Tag>
                            <Tag color={rel.prerelease ? 'orange' : 'green'} style={{ margin: 0 }}>
                              {rel.prerelease ? t('Preview') : t('Release')}
                            </Tag>
                            <Typography.Text strong>{rel.name}</Typography.Text>
                            {rel.publishedAt && (
                              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                                {new Date(rel.publishedAt).toLocaleDateString()}
                              </Typography.Text>
                            )}
                          </Space>
                        }
                        extra={
                          <Typography.Link href={rel.htmlUrl} target="_blank" rel="noreferrer">
                            {t('View on GitHub')}
                          </Typography.Link>
                        }
                      >
                        <SimpleMarkdownView markdown={rel.body || t('No release notes provided.')} />
                        {rel.assets && rel.assets.length > 0 && (
                          <div
                            style={{
                              marginTop: 10,
                              paddingTop: 8,
                              borderTop: '1px dashed rgba(128,128,128,0.2)',
                            }}
                          >
                            <Space size={[6, 6]} wrap>
                              {rel.assets.map((a) => (
                                <Button
                                  key={a.name}
                                  size="small"
                                  icon={<CloudDownloadOutlined />}
                                  href={a.downloadUrl}
                                  target="_blank"
                                  rel="noreferrer"
                                >
                                  {a.name} ({formatBytes(a.size)})
                                </Button>
                              ))}
                            </Space>
                          </div>
                        )}
                      </Card>
                    ))}
                  </div>
                ),
            },
          ]}
        />
      </Spin>
    </Modal>
  );
};
