# chksvs
utility tool to convert raw SVS data (stored by hadock) to csv files

the tool works as follow:
* read a list of files given as arguments to the command or from stdin if the no arguments are given
* checks that the first 4 characters (FCC) of the files are "SVS " (note the leading space).
if the FCC does not match, the files are skipped
* if the sequence counter is equal to 1, save the rest (after having skip the rest of the hadock headers) of the file as is in a file named <UPI>.ini.
Value of UPI is taken from the UPI present in the original filename
* if the sequence counter is greater than 1:
  * extract the metadata found after the hadock headers. these metadata are the headers found in the VMU packet of the original image
  * convert the binary values by block into csv
  * compute the directory where the files will be stored by dividing the sequence counter by the value given to the -p option
  * compute the final filename according to the information found in the metadata
  * store the metadata as XML next to the converted data as CSV.
* all the files processed by chksvs will be stored under the directory given to the -d option + the UPI. 
eg, if -d is set to /data/ and UPI to be processed is FOOBAR, then all the files will be stored under /data/FOOBAR
  
filename pattern is: `<source>_<upi>_<date_time>_<sequence>.<ext>`

<pre>
usage:
-d: directory where converted files are to be stored (default to os specific temp directory)
-k: keep invalid files (.bad)
-p: number of files per sub directories (default: 512)
-w: number of parallel workers (default: 10)
</pre>

examples
<pre>
linux$ chksvs -p 1024 -w 4 -d /data/svs/ file0...file1
linux$ find share/upi/XYZ/90/ | chksvs -k -p 64

powershell$ chksvs.exe -p 1024 -w 4 -d .\data\svs\ file0...file1
powershell$  Get-ChildItem -Path .\tmp\blocks\ -File | Resolve-Path -Relative | chksvs.exe -p 1024 -w 4 -d .\data\svs\
</pre>
